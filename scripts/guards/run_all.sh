#!/usr/bin/env bash
# run_all.sh — WS0 reality-guards runner (bd-ws0-guards-klz98.1).
#
# Runs every guard script in scripts/guards/*.sh (sorted, excluding itself).
# The CI job `reality-guards` (.github/workflows/ci.yml) calls exactly:
#     bash scripts/check_allowlists.sh
#     bash scripts/guards/run_all.sh
# and never needs editing again.
#
# PLUG-IN CONTRACT for later lanes (G1-G4, ...):
#   - Drop `<name>.sh` into scripts/guards/ (bash, `set -u`-clean, repo-root
#     independent: cd to the repo root yourself, as the existing guards do).
#   - Exit 0 = pass; any non-zero exit fails the reality-guards CI job. Print
#     violations to stderr with the allowlist file and waiver protocol named.
#   - Include your own mandatory canary (fixture under scripts/guards/testdata/)
#     and FAIL HARD if the canary does not fire — a guard that cannot
#     demonstrate a catch is a placebo with a green badge.
#   - Ratchet against your ci/allowlists/*.txt in both directions via
#     scripts/guards/lib/ratchet.sh (`ratchet_compare`), never via ad-hoc greps.
#   - No `br`, network, or Go toolchain dependence: CI runs this on a bare
#     ubuntu runner; bead-openness is checked by scripts/check_allowlists.sh
#     only where br exists.
#   - Helpers go in scripts/guards/lib/ and fixtures in scripts/guards/testdata/
#     (subdirectories are not globbed as guards).
set -u

GUARDS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SELF="$(basename "${BASH_SOURCE[0]}")"

shopt -s nullglob
guards=("$GUARDS_DIR"/*.sh)

failed=()
ran=0
for guard in "${guards[@]}"; do
  name="$(basename "$guard")"
  [ "$name" = "$SELF" ] && continue
  echo "=== reality-guard: $name ==="
  if bash "$guard"; then
    ran=$((ran + 1))
  else
    ran=$((ran + 1))
    failed+=("$name")
  fi
done

if [ "$ran" -eq 0 ]; then
  echo "run_all.sh: FAIL — no guard scripts found in $GUARDS_DIR" >&2
  exit 1
fi
if [ "${#failed[@]}" -gt 0 ]; then
  echo "run_all.sh: FAILED guards: ${failed[*]}" >&2
  exit 1
fi
echo "run_all.sh: OK ($ran guard(s) passed)"
