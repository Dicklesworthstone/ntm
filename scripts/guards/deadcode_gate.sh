#!/usr/bin/env bash
# deadcode_gate.sh — WS0-G1 dead-code gate (bd-ws0-guards-klz98.2).
#
# "Impressive but unreachable" is a build failure: any function under
# internal/ with no call path from ./cmd/ntm, and any internal/ package not in
# cmd/ntm's import graph, must carry a `ci/allowlists/deadcode.txt` line tied
# to an OPEN bead (both-direction ratchet via scripts/guards/lib/ratchet.sh).
#
# Analysis (needs the Go toolchain):
#   - golang.org/x/tools/cmd/deadcode, version PINNED by the `tool` directive
#     in go.mod (an @latest tool is itself drift), built for the HOST and run
#     once per target GOOS. Roots = ./cmd/ntm, WITHOUT -test: test binaries as
#     roots would mark every tested-but-orphaned engine live — the exact
#     pathology this gate exists to catch (bead review round 3).
#   - Multi-config: deadcode is valid for one GOOS/GOARCH at a time. We run
#     GOOS=linux, darwin, windows and INTERSECT: a function is "dead" only if
#     dead in EVERY config.
#     Q5 record (re-verified 2026-08-16 at implementation): windows-gated
#     non-test code EXISTS under internal/ (internal/hooks/procgroup_windows.go
#     per commit 85851434, plus internal/{robot,tmux,alerts,history,session,
#     config,supervisor,pipeline,cli,assignment}/*_windows.go) => three runs.
#     GOARCH is pinned to amd64 for determinism across dev/CI hosts; no
#     GOARCH-gated first-party file exists under internal/ or cmd/ (verified
#     by filename and //go:build grep at implementation time).
#   - Orphan-package check: deadcode only sees packages reachable in the
#     import graph, so a NEVER-imported package (C9's nine) produces zero
#     entries. `go list ./internal/...` minus `go list -deps ./cmd/ntm` fills
#     that blind spot; those violations use entry form `pkg:internal/<path>`.
#
# Entry format in ci/allowlists/deadcode.txt:
#   <file>.go:<Func>        e.g. internal/coordinator/conflicts.go:Engine.Run
#   pkg:internal/<path>     an entire internal package unreachable from cmd/ntm
#
# Known blind spots + compensations:
#   - reflect.Value.Call targets can be falsely dead => `# permanent:
#     reflection` allowlist section, reason names the reaching call site.
#     Cobra RunE indirection is NOT such a case: RTA tracks address-taken
#     dynamically-invoked funcs — proven by the canary before every run.
#   - Test helpers in non-test files are reported dead (no -test): either move
#     them into _test.go files or add a `# permanent: test-helper` entry
#     naming the reaching test.
#   - Generated files: excluded by deadcode's generated-file handling.
#
# Mandatory canary (scripts/guards/testdata/deadcode_canary/, standalone
# module): (a) OrphanExported must be reported dead, (b) cobra-RunE-only
# runEDispatch must NOT be, (c) tested-but-unwired TestedButUnwired must be.
# Any miss aborts the gate — a guard that cannot demonstrate a catch is a
# placebo with a green badge.
#
# Toolchain wiring: the bare-ubuntu `reality-guards` CI job has no Go; there
# this script validates allowlist entry shape only and exits 0. The full
# analysis is guaranteed in CI by the dedicated `reality-guards-go` job
# (.github/workflows/ci.yml), which sets up Go and runs this script.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1
# shellcheck source=lib/ratchet.sh
. "$REPO_ROOT/scripts/guards/lib/ratchet.sh"

ALLOWLIST="${DEADCODE_ALLOWLIST:-ci/allowlists/deadcode.txt}"
MODULE=github.com/Dicklesworthstone/ntm
GOOS_LIST="linux darwin windows"
CANARY_DIR=scripts/guards/testdata/deadcode_canary

# --- allowlist entry-shape validation (runs everywhere, incl. bare CI) ------
shape_bad="$(awk -F'\t' '/^#/ || /^[[:space:]]*$/ {next}
  $1 !~ /^(internal\/[A-Za-z0-9_\/.-]+\.go:[A-Za-z0-9_.$]+|pkg:internal\/[A-Za-z0-9_\/.-]+)$/ {print FNR": "$1}' "$ALLOWLIST")"
if [ -n "$shape_bad" ]; then
  echo "deadcode_gate: FAIL — malformed entry column(s) in $ALLOWLIST (want <file>.go:<Func> or pkg:internal/<path>):" >&2
  printf '%s\n' "$shape_bad" | sed 's/^/  /' >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "deadcode_gate.sh: OK (format-side only: no Go toolchain here; full analysis runs in the reality-guards-go CI job)"
  exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if ! go build -o "$tmp/deadcode" golang.org/x/tools/cmd/deadcode; then
  echo "deadcode_gate: FAIL — could not build pinned golang.org/x/tools/cmd/deadcode (see go.mod tool directive)" >&2
  exit 1
fi

# --- mandatory canary self-test ---------------------------------------------
if ! (cd "$CANARY_DIR" && "$tmp/deadcode" ./cmd/canary) > "$tmp/canary.out" 2>"$tmp/canary.err"; then
  echo "deadcode_gate: CANARY FAILURE — deadcode failed on $CANARY_DIR:" >&2
  cat "$tmp/canary.err" >&2
  exit 1
fi
grep -q 'unreachable func: OrphanExported' "$tmp/canary.out" || {
  echo "deadcode_gate: CANARY FAILURE — orphaned exported func not reported dead (case a)" >&2; exit 1; }
grep -q 'unreachable func: TestedButUnwired' "$tmp/canary.out" || {
  echo "deadcode_gate: CANARY FAILURE — tested-but-unwired func not reported dead (case c: the gate's seed class)" >&2; exit 1; }
if grep -q 'runEDispatch' "$tmp/canary.out"; then
  echo "deadcode_gate: CANARY FAILURE — cobra RunE-only func falsely reported dead (case b): deadcode version regressed on dynamic-call tracking" >&2
  exit 1
fi

# --- multi-GOOS analysis, intersected ---------------------------------------
for goos in $GOOS_LIST; do
  if ! GOOS="$goos" GOARCH=amd64 "$tmp/deadcode" -filter "^${MODULE}/internal/" ./cmd/ntm > "$tmp/raw.$goos" 2>"$tmp/err.$goos"; then
    echo "deadcode_gate: FAIL — deadcode analysis failed for GOOS=$goos:" >&2
    cat "$tmp/err.$goos" >&2
    exit 1
  fi
  # "internal/x/y.go:12:3: unreachable func: T.F" -> "internal/x/y.go:T.F"
  awk -F': unreachable func: ' 'NF==2 {p=$1; sub(/:[0-9]+:[0-9]+$/, "", p); print p ":" $2}' \
    "$tmp/raw.$goos" | sort -u > "$tmp/norm.$goos"
done
comm -12 "$tmp/norm.linux" "$tmp/norm.darwin" | comm -12 - "$tmp/norm.windows" > "$tmp/dead.txt"

# --- orphan-package check (deadcode's whole-package blind spot) -------------
comm -23 <(go list ./internal/... | sort) \
         <(go list -deps ./cmd/ntm | grep "^${MODULE}/internal" | sort) \
  | sed "s|^${MODULE}/|pkg:|" >> "$tmp/dead.txt"

# --- join violations with the allowlist's bead column, then ratchet ---------
# permanent-section entries are exempt from the ratchet (tool blind spots);
# a violation with no allowlist line becomes "<entry>\t-" => NEW DEBT.
awk -F'\t' '
  NR==FNR {
    if ($0 ~ /^#[[:space:]]*permanent:/) { perm=1; next }
    if ($0 ~ /^#/ || $0 ~ /^[[:space:]]*$/) { next }
    if (perm && $2 == "permanent") { skip[$1]=1; next }
    bead[$1]=$2; next
  }
  {
    if ($0 in skip) next
    if ($0 in bead) print $0 "\t" bead[$0]
    else            print $0 "\t-"
  }
' "$ALLOWLIST" "$tmp/dead.txt" | sort -u > "$tmp/pairs.txt"

if ! ratchet_compare "$ALLOWLIST" "$tmp/pairs.txt"; then
  echo "deadcode_gate.sh: FAILED — see ci/allowlists/README.md (waiver protocol: bead FIRST, then the allowlist line)" >&2
  exit 1
fi
echo "deadcode_gate.sh: OK (canary fired on a+c, passed b; $(wc -l < "$tmp/dead.txt" | tr -d ' ') dead entries all ratchet-matched against $ALLOWLIST)"
