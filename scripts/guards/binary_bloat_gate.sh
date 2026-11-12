#!/usr/bin/env bash
# binary_bloat_gate.sh — WS0 tracked binary & archive bloat guard
# (bd-rc-track-d-release-docs-3ho85.2 / GH #346).
#
# Fails CI if git ls-files tracks:
#   - any ELF, Mach-O, or PE/COFF executable (`binary:<path>`)
#   - any single non-binary file > 2 MiB (`oversize:<path>`), excluding the
#     append-only Beads issue ledger (`.beads/*.jsonl`)
#
# Ratcheted in BOTH directions against ci/allowlists/binary_bloat.txt via
# scripts/guards/lib/ratchet.sh. Includes a mandatory firing canary anchored at
# scripts/guards/testdata/binary_bloat_canary/ that verifies ELF, Mach-O, PE,
# and >2 MiB detections all fire before scanning the repository.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1
# shellcheck source=lib/ratchet.sh
. "$REPO_ROOT/scripts/guards/lib/ratchet.sh"

ALLOWLIST="${BINARY_BLOAT_ALLOWLIST:-ci/allowlists/binary_bloat.txt}"
CANARY_DIR="scripts/guards/testdata/binary_bloat_canary"
MAX_BYTES=2097152 # 2 MiB

if [ ! -f "$CANARY_DIR/README.txt" ]; then
  echo "binary_bloat_gate: CANARY FAILURE — missing $CANARY_DIR/README.txt" >&2
  exit 1
fi

# scan_list <root-dir> <newline-separated-relpaths-file>
# Emits "<class>:<relpath><TAB><bytes>" for each violating file.
scan_list() {
  local root="$1" list_file="$2" rel full sz magic
  while IFS= read -r rel; do
    [ -n "$rel" ] || continue
    case "$rel" in
      scripts/guards/testdata/binary_bloat_canary/*) continue ;;
    esac
    full="$root/$rel"
    [ -f "$full" ] || continue
    magic="$(od -An -tx1 -N4 "$full" 2>/dev/null | tr -d ' \n')"
    sz="$(wc -c < "$full" | tr -d ' ')"
    case "$magic" in
      7f454c46|cffaedfe|cefaedfe|feedfacf|feedface|cafebabe|4d5a9000)
        printf 'binary:%s\t%s\n' "$rel" "$sz"
        continue
        ;;
    esac
    case "$rel" in
      .beads/*.jsonl) continue ;;
    esac
    if [ "${sz:-0}" -gt "$MAX_BYTES" ]; then
      printf 'oversize:%s\t%s\n' "$rel" "$sz"
    fi
  done < "$list_file"
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- mandatory canary -------------------------------------------------------
mkdir -p "$TMP/canary/bin" "$TMP/canary/.beads"
printf '\177ELFcanary' > "$TMP/canary/bin/fake_elf"
printf '\317\372\355\376canary' > "$TMP/canary/bin/fake_macho"
printf 'MZ\220\000canary' > "$TMP/canary/bin/fake_pe.exe"
dd if=/dev/zero of="$TMP/canary/big.blob" bs=1048576 count=3 status=none
dd if=/dev/zero of="$TMP/canary/.beads/issues.jsonl" bs=1048576 count=3 status=none
printf 'package ok\n' > "$TMP/canary/ok.go"
cat > "$TMP/canary_list.txt" <<'EOF_CANARY'
bin/fake_elf
bin/fake_macho
bin/fake_pe.exe
big.blob
.beads/issues.jsonl
ok.go
EOF_CANARY

canary_out="$(scan_list "$TMP/canary" "$TMP/canary_list.txt" | cut -f1 | LC_ALL=C sort -u)"
for want in '^binary:bin/fake_elf$' '^binary:bin/fake_macho$' '^binary:bin/fake_pe\.exe$' '^oversize:big\.blob$'; do
  if ! printf '%s\n' "$canary_out" | grep -qE "$want"; then
    echo "binary_bloat_gate: CANARY FAILURE — expected '$want' to fire" >&2
    printf '%s\n' "$canary_out" | sed 's/^/  got: /' >&2
    exit 1
  fi
done
for forbid in 'issues\.jsonl' 'ok\.go'; do
  if printf '%s\n' "$canary_out" | grep -qE "$forbid"; then
    echo "binary_bloat_gate: CANARY FAILURE — false positive on '$forbid'" >&2
    exit 1
  fi
done

# --- real repository scan + both-direction ratchet --------------------------
git ls-files > "$TMP/tracked.txt"
scan_list "$REPO_ROOT" "$TMP/tracked.txt" > "$TMP/raw_hits.txt"

total_bloat_bytes="$(awk -F'\t' '{s += $2} END {print s+0}' "$TMP/raw_hits.txt")"
hit_count="$(wc -l < "$TMP/raw_hits.txt" | tr -d ' ')"

cut -f1 "$TMP/raw_hits.txt" | LC_ALL=C sort -u | awk '{print $0 "\t-"}' > "$TMP/cur.txt"
ratchet_extract_pairs "$ALLOWLIST" '(binary|oversize):' > "$TMP/allowed.txt"
awk -F'\t' 'NR==FNR { bead[$1]=$2; next } { e=$1; if (e in bead) print e "\t" bead[e]; else print e "\t-" }' \
  "$TMP/allowed.txt" "$TMP/cur.txt" | LC_ALL=C sort -u > "$TMP/joined.txt"

if ! ratchet_compare "$ALLOWLIST" "$TMP/joined.txt" '(binary|oversize):'; then
  echo "binary_bloat_gate.sh: FAILED (tracked_bloat_files=$hit_count tracked_bloat_bytes=$total_bloat_bytes)" >&2
  exit 1
fi

echo "binary_bloat_gate.sh: OK (canary fired; tracked_bloat_files=$hit_count tracked_bloat_bytes=$total_bloat_bytes ratcheted against $ALLOWLIST)"
