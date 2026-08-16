#!/usr/bin/env bash
# contracts_lint.sh — WS0-G6 single-definition contracts lint (bd-ws0-guards-klz98.7).
#
# Prevents "two ends of one contract drift apart" (B4 label delimiter, B5 dual
# manifest writers, D1 per-surface pagination flags). Each cross-cutting contract
# has ONE canonical definition point:
#   - session-label delimiter: internal/config/label.go today (W1's B4 lane may
#     move/extend it as internal/session/label.go — both are exempt helper files)
#   - monitor manifest construction: internal/resilience/ (SpawnManifest writer)
#   - pagination flags: internal/robot/schema.go registry flags (enforced by the
#     WS3 registry-walk conformance tests, tracked here as rollout:* allowlist
#     lines only — not computed by this grep lint)
#
# Computed violation classes (ratcheted both directions against
# ci/allowlists/contracts.txt under the shared infra rules):
#   label-split:<file>:<func>:<literal>  raw "__"/"--" delimiter literal inside a
#                                        function whose name contains "label",
#                                        outside the canonical helper files
#   manifest-writer:<file>               resilience.SpawnManifest{...} composite
#                                        literal outside internal/resilience/
#
# Self-tests first (mandatory canary): the fixture in
# scripts/guards/testdata/contracts_canary/ carries a stray "__" label split, a
# stray "--" label join, and a SpawnManifest literal; the lint must fire on all
# of them or it aborts.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
# shellcheck source=lib/ratchet.sh
. "$REPO_ROOT/scripts/guards/lib/ratchet.sh"

ALLOWLIST="${CONTRACTS_ALLOWLIST:-ci/allowlists/contracts.txt}"
CANARY_DIR="scripts/guards/testdata/contracts_canary"

HELPER_FILES_RE='(internal/config/label\.go|internal/session/label\.go)$'

# scan <root>... — emits "<entry>" lines (one per violation, deduped by sort -u later)
scan() {
  local roots=("$@")

  # -- label-split: raw "__"/"--" literals inside label-handling functions -----
  find "${roots[@]}" -name '*.go' ! -name '*_test.go' -type f 2>/dev/null \
    | grep -Ev "$HELPER_FILES_RE" \
    | while IFS= read -r f; do
        awk -v file="$f" '
          /^func / {
            fn = $0
            sub(/^func[[:space:]]+(\([^)]*\)[[:space:]]*)?/, "", fn)
            sub(/\(.*/, "", fn)
          }
          fn ~ /[Ll]abel/ && /"(__|--)"/ {
            lit = ($0 ~ /"__"/) ? "__" : "--"
            print "label-split:" file ":" fn ":" lit
          }
        ' "$f"
      done

  # -- manifest-writer: SpawnManifest composite literals outside resilience ----
  grep -rn --include='*.go' 'SpawnManifest{' "${roots[@]}" 2>/dev/null \
    | grep -v '_test\.go:' \
    | grep -v '^internal/resilience/' \
    | cut -d: -f1 \
    | sed 's/^/manifest-writer:/'
}

# --- mandatory canary -------------------------------------------------------
canary_out="$(scan "$CANARY_DIR" | sort -u)"
for want in 'label-split:.*extractCanaryLabel:__' 'label-split:.*makeCanaryLabel:--' 'manifest-writer:.*canary\.go'; do
  if ! printf '%s\n' "$canary_out" | grep -qE "$want"; then
    echo "contracts_lint: CANARY FAILURE — expected violation matching '$want' not detected in $CANARY_DIR" >&2
    printf '%s\n' "$canary_out" | sed 's/^/  got: /' >&2
    exit 1
  fi
done

# --- real scan + both-direction ratchet -------------------------------------
cur_tmp="$(mktemp)"
trap 'rm -f "$cur_tmp"' EXIT
# rollout:* allowlist entries are consumed by the WS3 conformance tests, not this
# lint, so the ratchet is restricted to the classes this script computes.
scan internal cmd | sort -u | awk '{print $0 "\t-"}' > "$cur_tmp"

pair_allow_tmp="$(mktemp)"
ratchet_extract_pairs "$ALLOWLIST" '(label-split|manifest-writer):' > "$pair_allow_tmp"
# current violations carry no bead association; compare entry-to-entry by joining
# the allowlist's bead ids onto matching entries.
join_tmp="$(mktemp)"
awk -F'\t' 'NR==FNR { bead[$1]=$2; next } { e=$1; if (e in bead) print e "\t" bead[e]; else print e "\t-" }' \
  "$pair_allow_tmp" "$cur_tmp" | sort -u > "$join_tmp"

rc=0
if ! ratchet_compare "$ALLOWLIST" "$join_tmp" '(label-split|manifest-writer):'; then
  rc=1
fi
rm -f "$pair_allow_tmp" "$join_tmp"

if [ "$rc" -ne 0 ]; then
  echo "contracts_lint.sh: FAILED" >&2
  exit 1
fi
echo "contracts_lint.sh: OK (canary fired; tree clean against $ALLOWLIST)"
