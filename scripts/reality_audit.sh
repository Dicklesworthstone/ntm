#!/usr/bin/env bash
# reality_audit.sh — the Re-Audit Ritual, scripted (WS0-G7b, bd-ws0-guards-klz98.8;
# recurring obligation: bd-swarm-execution-reaudit-bqw8x.10).
#
# A time-boxed, seeded, reproducible "where are we REALLY?" release gate.
# Run before R2 and R3 release notes are written, and quarterly thereafter.
# This is the R0 SKELETON — refined each run; refinements land in this file.
#
# Usage:
#   bash scripts/reality_audit.sh <tag> [--notes <draft-release-notes.md>]
#
# Budget: 90 minutes TOTAL, per-step budgets below. The script stamps wall-clock
# per step; an overrun is ITSELF A FINDING and is written into the verdict.
#
# Protocol (bd-swarm-execution-reaudit-bqw8x.10, verbatim mapping):
#   1. (10 min) Rebuild the Vision Checklist mechanically from claim-source
#      headings; diff against the previous run's committed checklist.
#   2. (15 min) Run the WS0 gate suite + allowlist line counts; ANY growth
#      since the previous audit is a finding.
#   3. (30 min) Behavioral spot-probes: 10 checklist rows sampled with a seed
#      derived from the tag (reproducible, cannot be cherry-picked), exercised
#      against the BUILT RELEASE ARTIFACT — the script emits the probe table;
#      executing the probes is the operator/agent half of the ritual.
#   4. (20 min) Ledger audit: 10 seeded-sampled beads closed since the last
#      audit; each must name its Proof test and the test must exist in
#      `go test -list` output.
#   5. (15 min) Diff verdict: new NO_BEAD gaps -> beads filed ON THE SPOT;
#      draft release notes may not use completeness language for any surface
#      with an open gap (grepped here when --notes is given).
#
# Output: docs/reality/checklist-<tag>.md + docs/reality/audit-<tag>.md,
# committed with the release. Machine-readable markers (`allowlist-lines:`,
# `generated:`) let the NEXT run diff against this one.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

TAG="${1:-}"
if [ -z "$TAG" ]; then
  echo "usage: bash scripts/reality_audit.sh <tag> [--notes <file>]" >&2
  exit 2
fi
shift
NOTES_FILE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --notes) NOTES_FILE="${2:-}"; shift 2 ;;
    *) echo "reality_audit: unknown arg: $1" >&2; exit 2 ;;
  esac
done

OUT_DIR="${REALITY_OUT_DIR:-docs/reality}" # override only for self-testing the script
mkdir -p "$OUT_DIR"
CHECKLIST="$OUT_DIR/checklist-$TAG.md"
AUDIT="$OUT_DIR/audit-$TAG.md"

# Claim sources: every doc whose headings promise user-facing capability.
CLAIM_SOURCES=(README.md docs/ORCHESTRATION_FEATURES.md SKILL.md command_palette.md)

# Seed: deterministic from the tag name so the sample cannot be cherry-picked.
SEED="$(printf '%s' "$TAG" | cksum | awk '{print $1}')"

FINDINGS=()
finding() { FINDINGS+=("$*"); echo "FINDING: $*" >&2; }

RITUAL_START=$(date +%s)
STEP_START=$RITUAL_START
STEP_TIMES=()
step_done() { # step_done <label> <budget-min>
  local now; now=$(date +%s)
  local mins=$(( (now - STEP_START + 59) / 60 ))
  STEP_TIMES+=("$1: ${mins}m (budget $2m)")
  if [ "$mins" -gt "$2" ]; then
    finding "time-box overrun: step '$1' took ${mins}m > ${2}m budget — the ritual itself is broken"
  fi
  STEP_START=$now
}

# seeded_sample <n> — deterministic sample of n stdin lines: rank by
# cksum("<seed>:<line>"), take the n smallest. Reproducible for a given tag.
seeded_sample() {
  local n="$1" line
  while IFS= read -r line; do
    printf '%s\t%s\n' "$(printf '%s:%s' "$SEED" "$line" | cksum | awk '{print $1}')" "$line"
  done | sort -n | head -n "$n" | cut -f2-
}

# ---------------------------------------------------------------------------
# Step 1 (10 min): rebuild the Vision Checklist mechanically; diff vs previous.
# ---------------------------------------------------------------------------
echo "=== step 1: vision checklist ($CHECKLIST) ==="
{
  echo "# Vision Checklist — $TAG"
  echo ""
  echo "generated: $(date -u +%Y-%m-%dT%H:%M:%SZ) by scripts/reality_audit.sh (seed $SEED)"
  echo ""
  for src in "${CLAIM_SOURCES[@]}"; do
    if [ ! -f "$src" ]; then
      echo "<!-- claim source missing: $src -->"
      continue
    fi
    # Claim rows = section headings (##..####). Top-level titles are identity, not claims.
    grep -n '^##\{1,3\} ' "$src" | sed "s|^\([0-9]*\):#*[[:space:]]*|- $src:\1 |"
  done
} > "$CHECKLIST"
for src in "${CLAIM_SOURCES[@]}"; do
  [ -f "$src" ] || finding "claim source missing: $src (checklist row set is incomplete)"
done
ROW_COUNT=$(grep -c '^- ' "$CHECKLIST" || true)
echo "checklist rows: $ROW_COUNT"
[ "$ROW_COUNT" -ge 10 ] || finding "checklist has only $ROW_COUNT rows — extraction is broken, not the vision"

# Diff against the most recent previous checklist (row text only, line numbers drift).
PREV_CHECKLIST="$(ls -t "$OUT_DIR"/checklist-*.md 2>/dev/null | grep -vF "checklist-$TAG.md" | head -1 || true)"
CHECKLIST_DIFF=""
if [ -n "$PREV_CHECKLIST" ]; then
  CHECKLIST_DIFF="$(diff \
    <(sed -n 's/^- [^ ]* //p' "$PREV_CHECKLIST" | sort -u) \
    <(sed -n 's/^- [^ ]* //p' "$CHECKLIST" | sort -u) || true)"
  [ -z "$CHECKLIST_DIFF" ] || echo "checklist drift vs $PREV_CHECKLIST (review in step 5):"
  printf '%s\n' "$CHECKLIST_DIFF"
else
  echo "no previous checklist found — this run is the new baseline"
fi
step_done "1-checklist" 10

# ---------------------------------------------------------------------------
# Step 2 (15 min): WS0 gate suite + allowlist burndown; growth is a finding.
# ---------------------------------------------------------------------------
echo "=== step 2: WS0 gate suite + allowlist counts ==="
GATES_OK=yes
bash scripts/check_allowlists.sh || { GATES_OK=no; finding "check_allowlists.sh failed"; }
bash scripts/guards/run_all.sh   || { GATES_OK=no; finding "guard suite (scripts/guards/run_all.sh) failed"; }
ALLOWLIST_TOTAL=$(cat ci/allowlists/*.txt 2>/dev/null | grep -cv '^\s*\(#\|$\)' || true)
ALLOWLIST_WC="$(wc -l ci/allowlists/*.txt 2>/dev/null || true)"
echo "$ALLOWLIST_WC"
echo "allowlist entries (non-comment): $ALLOWLIST_TOTAL"
PREV_AUDIT="$(ls -t "$OUT_DIR"/audit-*.md 2>/dev/null | grep -vF "audit-$TAG.md" | head -1 || true)"
if [ -n "$PREV_AUDIT" ]; then
  PREV_TOTAL="$(sed -n 's/^allowlist-lines: //p' "$PREV_AUDIT" | head -1)"
  if [ -n "$PREV_TOTAL" ] && [ "$ALLOWLIST_TOTAL" -gt "$PREV_TOTAL" ]; then
    finding "allowlist growth: $PREV_TOTAL -> $ALLOWLIST_TOTAL entries since $PREV_AUDIT — debt increased"
  fi
fi
step_done "2-gates" 15

# ---------------------------------------------------------------------------
# Step 3 (30 min): seeded behavioral spot-probes against the BUILT artifact.
# ---------------------------------------------------------------------------
echo "=== step 3: seeded spot-probe sample (seed $SEED) ==="
PROBE_ROWS="$(sed -n 's/^- //p' "$CHECKLIST" | seeded_sample 10)"
printf '%s\n' "$PROBE_ROWS"
echo "(exercise each row against the built release artifact, then fill the probe table in $AUDIT)"
step_done "3-probes" 30

# ---------------------------------------------------------------------------
# Step 4 (20 min): ledger audit — 10 seeded-sampled closed beads need a live Proof test.
# ---------------------------------------------------------------------------
echo "=== step 4: ledger audit ==="
LEDGER_ROWS=""
LEDGER_SCOPE="all-recent (no previous audit found — era unclassified)"
if command -v br >/dev/null 2>&1; then
  # Sample scope (bd-yz3nb): only beads closed SINCE the previous committed
  # audit's `generated:` timestamp. Pre-discipline closes (before proof-naming
  # existed) are excluded instead of polluting the sample with known
  # NO_PROOF_NAMED noise. Falls back to the recent-closed window on the first
  # ever run. H10's matrix-conformance gate automates the matrix half.
  PREV_AUDIT_DATE="$(sed -n 's/^generated: \([0-9TZ:.-]*\).*/\1/p' \
      $(ls "$OUT_DIR"/audit-*.md 2>/dev/null | grep -vF "audit-$TAG.md" || true) /dev/null 2>/dev/null \
    | sort | tail -1)"
  if [ -n "$PREV_AUDIT_DATE" ]; then
    LEDGER_SCOPE="closed since previous audit ($PREV_AUDIT_DATE) — post-discipline era only"
    CLOSED_IDS="$(br list --status=closed --sort updated_at --limit=500 --format csv --fields id,closed_at 2>/dev/null \
      | awk -F, -v d="$PREV_AUDIT_DATE" 'NR>1 && $1 ~ /^bd-/ { ca=$2; gsub(/\+00:00$/,"Z",ca); if (ca >= d) print $1 }' \
      | sort -u || true)"
  else
    CLOSED_IDS="$(br list --status=closed --limit=200 2>/dev/null | grep -o 'bd-[a-z0-9][a-z0-9._-]*' | sort -u || true)"
  fi
  echo "ledger scope: $LEDGER_SCOPE ($(printf '%s\n' "$CLOSED_IDS" | sed '/^$/d' | wc -l | tr -d ' ') candidates)"
  SAMPLE_IDS="$(printf '%s\n' "$CLOSED_IDS" | sed '/^$/d' | seeded_sample 10)"
  # NTM_INTEGRATION_TESTS=1: env-gated packages (tests/integration) skip
  # wholesale without it, so their tests would vanish from -list and every
  # integration-test proof would false-positive as PROOF_MISSING (bd-m9zpa).
  # Listing only enumerates test names; it does not execute the tests.
  TEST_LIST="$(NTM_INTEGRATION_TESTS=1 go test ./... -list '.*' 2>/dev/null | grep -E '^(Test|Fuzz|Example)' | sort -u || true)"
  # check_proofs <bead-id> — echo the first named (Test|Fuzz) function that
  # `go test -list` confirms live, or "MISSING <first-named>" if names exist
  # but none are live, or nothing if the close names no test at all.
  check_proofs() {
    local bid="$1" p first=""
    while IFS= read -r p; do
      [ -n "$p" ] || continue
      [ -n "$first" ] || first="$p"
      if printf '%s\n' "$TEST_LIST" | grep -qxF "$p"; then echo "$p"; return 0; fi
    done < <(br show "$bid" 2>/dev/null | grep -oE '(Test|Fuzz)[A-Z_][A-Za-z0-9_]*' | sort -u)
    [ -z "$first" ] || echo "MISSING $first"
  }
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    proof="$(check_proofs "$id")"
    case "$proof" in
      "")
        # No Test function named verbatim. Accept an explicit delegated-proof
        # pointer the grep can follow: "proof ... bd-xxxx" in the close text,
        # where the SIBLING bead names a live Test function.
        delegate="$(br show "$id" 2>/dev/null | grep -ioE 'proofs?[^.]*bd-[a-z0-9][a-z0-9._-]*' | grep -oE 'bd-[a-z0-9][a-z0-9._-]*' | head -1 || true)"
        dproof=""
        [ -z "$delegate" ] || dproof="$(check_proofs "$delegate")"
        case "$dproof" in
          ""|MISSING*)
            status="NO_PROOF_NAMED"
            finding "ledger: closed bead $id names no live Proof test (post-discipline close must name a Test function verbatim or a followable delegated-proof bead)"
            ;;
          *) status="OK (delegated to $delegate: $dproof)" ;;
        esac
        ;;
      MISSING*)
        status="PROOF_MISSING (${proof#MISSING } not in go test -list)"
        finding "ledger: closed bead $id Proof test ${proof#MISSING } not found by 'go test -list'"
        ;;
      *) status="OK ($proof)" ;;
    esac
    LEDGER_ROWS="$LEDGER_ROWS| $id | $status |"$'\n'
    echo "$id -> $status"
  done <<<"$SAMPLE_IDS"
else
  finding "br not installed — ledger audit (step 4) skipped; run where br exists"
fi
step_done "4-ledger" 20

# ---------------------------------------------------------------------------
# Step 5 (15 min): diff verdict + completeness-language check + audit doc.
# ---------------------------------------------------------------------------
echo "=== step 5: verdict ==="
COMPLETENESS_HITS=""
if [ -n "$NOTES_FILE" ]; then
  if [ -f "$NOTES_FILE" ]; then
    COMPLETENESS_HITS="$(grep -inE '\b(full|fully|complete|completely|all|every|100%)\b' "$NOTES_FILE" || true)"
    [ -z "$COMPLETENESS_HITS" ] || finding "draft notes use completeness language (verify no open gap on those surfaces): see audit doc"
  else
    finding "--notes file not found: $NOTES_FILE"
  fi
fi
RITUAL_END=$(date +%s)
TOTAL_MIN=$(( (RITUAL_END - RITUAL_START + 59) / 60 ))
[ "$TOTAL_MIN" -le 90 ] || finding "ritual total ${TOTAL_MIN}m exceeds the 90-minute budget — that overrun is itself a finding"

{
  echo "# Reality Audit — $TAG"
  echo ""
  echo "generated: $(date -u +%Y-%m-%dT%H:%M:%SZ) by scripts/reality_audit.sh"
  echo "seed: $SEED"
  echo "allowlist-lines: $ALLOWLIST_TOTAL"
  echo "gates: $GATES_OK"
  echo "total-minutes: $TOTAL_MIN (budget 90)"
  echo ""
  echo "## Step timings"
  for t in "${STEP_TIMES[@]}"; do echo "- $t"; done
  echo ""
  echo "## Checklist drift (vs ${PREV_CHECKLIST:-none — baseline run})"
  echo '```diff'
  printf '%s\n' "${CHECKLIST_DIFF:-<no drift>}"
  echo '```'
  echo ""
  echo "## Allowlist burndown"
  echo '```'
  printf '%s\n' "$ALLOWLIST_WC"
  echo '```'
  echo ""
  echo "## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)"
  echo "| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |"
  echo "|---|-----------|--------------------------------------------------|----------|"
  i=0
  while IFS= read -r row; do
    [ -n "$row" ] || continue
    i=$((i + 1))
    echo "| $i | $row | UNPROBED | |"
  done <<<"$PROBE_ROWS"
  echo ""
  echo "## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)"
  echo ""
  echo "scope: $LEDGER_SCOPE"
  echo ""
  echo "| bead | proof status |"
  echo "|------|--------------|"
  printf '%s' "$LEDGER_ROWS"
  echo ""
  echo "## Completeness-language check (draft notes: ${NOTES_FILE:-not provided})"
  echo '```'
  printf '%s\n' "${COMPLETENESS_HITS:-<none>}"
  echo '```'
  echo ""
  echo "## Findings"
  if [ "${#FINDINGS[@]}" -eq 0 ]; then
    echo "- none (probe verdicts above may still add findings)"
  else
    for f in "${FINDINGS[@]}"; do echo "- $f"; done
  fi
  echo ""
  echo "## Verdict"
  echo "- New NO_BEAD gaps found: file beads ON THE SPOT (label reality-bridge) and list IDs here."
  echo "- Release notes may not use completeness language for any surface with an open gap."
} > "$AUDIT"

echo "reality_audit.sh: wrote $CHECKLIST and $AUDIT (${#FINDINGS[@]} scripted finding(s), ${TOTAL_MIN}m)"
echo "commit both files with the release; fill UNPROBED rows before writing release notes."
[ "${#FINDINGS[@]}" -eq 0 ] || exit 1
exit 0
