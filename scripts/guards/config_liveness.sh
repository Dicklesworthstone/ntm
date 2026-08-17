#!/usr/bin/env bash
# config_liveness.sh — WS0-G2 config-key liveness guard (bd-ws0-guards-klz98.3).
#
# A knob with no reader can no longer merge silently. The gate proper is
# internal/config/liveness_test.go (TestConfigKeyLiveness + its mandatory
# canary TestConfigKeyLivenessCanary): it reflection-walks the parsed Config
# struct's leaf keys and requires every key to be CLAIMED via
# config.RegisterReader("section.key", realReaderFuncRef) or allowlisted in
# ci/allowlists/config.txt (bead-first waiver protocol).
#
# ADAPTATION recorded per the lane brief: the walk needs reflection over the
# Go struct, so the both-direction ratchet is implemented IN the Go test with
# the exact ratchet.sh semantics (unlisted unclaimed key = NEW DEBT; listed
# key that is claimed/gone = STALE, delete in this PR) instead of via
# ratchet_compare here. Toolchain wiring: the bare-ubuntu `reality-guards` CI
# job has no Go, so there this script validates the allowlist's entry shape
# only; the full test is guaranteed in CI twice — by the ordinary `test` job
# (go test ./...) and by the dedicated `reality-guards-go` job, which runs
# this script with Go present.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

ALLOWLIST=ci/allowlists/config.txt

# --- allowlist entry-shape validation (runs everywhere, incl. bare CI) ------
# Entries are dotted TOML leaf-key paths, e.g. "integrations.caut.enabled".
shape_bad="$(awk -F'\t' '/^#/ || /^[[:space:]]*$/ {next}
  $1 !~ /^[a-z0-9_]+(\.[a-z0-9_-]+)*$/ {print FNR": "$1}' "$ALLOWLIST")"
if [ -n "$shape_bad" ]; then
  echo "config_liveness: FAIL — malformed entry column(s) in $ALLOWLIST (want a dotted config key path):" >&2
  printf '%s\n' "$shape_bad" | sed 's/^/  /' >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "config_liveness.sh: OK (format-side only: no Go toolchain here; the liveness test runs in the test and reality-guards-go CI jobs)"
  exit 0
fi

# -run is a prefix regex: matches TestConfigKeyLiveness AND the mandatory
# canary TestConfigKeyLivenessCanary; the canary failing fails this guard.
if ! go test ./internal/config -run 'TestConfigKeyLiveness' -count=1; then
  echo "config_liveness.sh: FAILED — see internal/config/liveness_test.go output above (waiver protocol: bead FIRST, then the ci/allowlists/config.txt line; the real fix is a RegisterReader claim)" >&2
  exit 1
fi
echo "config_liveness.sh: OK (canary + liveness ratchet green against $ALLOWLIST)"
