#!/usr/bin/env bash
# tagged_vet.sh — tagged-test compile gate (bd-xj28x).
#
# WHY: nothing used to compile test files under build tags. The G1 dead-code
# burndown deleted helpers whose only callers were tagged tests
# (internal/tmux capture wrappers; ensemble.DryRunPlan.Validate;
# checkpoint.NewStorageWithDir) and go build / go vet / CI all stayed green —
# 3 tagged tests in internal/tmux plus 3 e2e files rotted silently. `go vet`
# DOES compile test files for the tags it is given, so vetting each tag set
# is a cheap compile gate.
#
# TAG SETS: every build tag that gates test code in this repo must appear in
# TAG_SETS below (combined sets cover files with `//go:build a && b`). When
# adding a new test-gating tag, add it here — the tag census check at the
# bottom fails if a tagged *_test.go file uses a tag no set covers.
#
# Toolchain wiring (same adaptation as config_liveness.sh): the bare-ubuntu
# `reality-guards` CI job has no Go, so this script degrades to a no-op
# there; the full gate is guaranteed by the `reality-guards-go` CI job, which
# runs it with Go present.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

if ! command -v go >/dev/null 2>&1; then
  echo "tagged_vet.sh: OK (no Go toolchain here; the tagged vet gate runs in the reality-guards-go CI job)"
  exit 0
fi

# --- mandatory canary: the guard must demonstrate a catch -------------------
CANARY_DIR="scripts/guards/testdata/tagged_vet_canary"
if go vet -C "$CANARY_DIR" -tags integration ./... >/dev/null 2>&1; then
  echo "tagged_vet.sh: FAIL — canary did not fire: 'go vet -tags integration' accepted $CANARY_DIR, which contains an intentionally broken tagged test. The guard is a placebo; do not weaken the canary." >&2
  exit 1
fi
if ! go vet -C "$CANARY_DIR" ./... >/dev/null 2>&1; then
  echo "tagged_vet.sh: FAIL — canary fixture $CANARY_DIR no longer builds UNTAGGED; fix the fixture so only the tagged file is broken." >&2
  exit 1
fi

# --- the gate: vet every test-gating tag set ---------------------------------
# "load" gates internal/serve/ws_load_test.go; "e2e,ensemble_experimental"
# also covers files tagged plain "e2e" and (via the untagged-package compile
# of internal/ensemble deps) pairs with "ensemble_experimental" alone.
TAG_SETS=(
  "integration"
  "e2e,ensemble_experimental"
  "load"
)
failed=()
for tags in "${TAG_SETS[@]}"; do
  echo "tagged_vet: go vet -tags $tags ./..."
  if ! go vet -tags "$tags" ./...; then
    failed+=("$tags")
  fi
done
if [ "${#failed[@]}" -gt 0 ]; then
  echo "tagged_vet.sh: FAIL — tagged test code does not compile under -tags: ${failed[*]}. Fix the tagged tests in the same PR that changed the code they call (bd-xj28x)." >&2
  exit 1
fi

# --- tag census: a new test-gating tag must be added to TAG_SETS -------------
covered=" integration e2e ensemble_experimental load "
# git-tracked files only: the repo carries a local .gomodcache/ whose
# third-party tests use tags (sqlite_*, goexperiment.*) we must not gate on.
uncovered="$(git grep -h '^//go:build ' -- '*_test.go' 2>/dev/null \
  | sed 's|^//go:build ||' \
  | tr '&|()!' ' ' \
  | tr ' ' '\n' \
  | sed '/^$/d' | sort -u \
  | while read -r tag; do
      case "$tag" in
        # GOOS/GOARCH and race are supplied by the platform/toolchain, not -tags.
        linux|darwin|windows|freebsd|openbsd|netbsd|plan9|solaris|js|wasip1|aix|android|ios|amd64|arm64|386|arm|race|cgo|unix|go1.*) continue ;;
        # Deliberate quarantine, NOT a gate gap: the pre-18ae64f3 scenario
        # harness (e2e/scenario_harness*.go) was parked behind this tag when
        # harness.go superseded it and no longer compiles (bd-yhc8r tracks
        # deleting it). Do not add new code under this tag.
        legacy_scenario_harness) continue ;;
      esac
      case "$covered" in
        *" $tag "*) ;;
        *) echo "$tag" ;;
      esac
    done)"
if [ -n "$uncovered" ]; then
  echo "tagged_vet.sh: FAIL — test files use build tag(s) not covered by TAG_SETS in scripts/guards/tagged_vet.sh:" >&2
  printf '  %s\n' $uncovered >&2
  echo "Add the tag to TAG_SETS so its test code is compile-gated." >&2
  exit 1
fi

echo "tagged_vet.sh: OK (canary fired; tagged test code compiles under: ${TAG_SETS[*]})"
