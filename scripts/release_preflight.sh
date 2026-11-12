#!/usr/bin/env bash
# release_preflight.sh — WS0-G4 release-artifact lockstep pre-flight
# (bd-ws0-guards-klz98.5, REDESIGNED rev 3).
#
# Closes root-cause mechanism #5: hand-maintained artifact lists let finished
# deliverables (web UI, VSCode extension) never ship. Guard-quality lesson
# recorded on the bead: rev 2 specced this guard onto release.yml — a pipeline
# AGENTS.md forbids (releases are DSR-only) — i.e. a guard against the wrong
# release mechanism is itself a placebo. Hence the firing-canary rule.
#
# Runs in TWO places:
#   (a) CI job `release-preflight` on every push:  --self-test  then  --dry-run
#   (b) DSR release checklist STEP 0 (AGENTS.md):  scripts/release_preflight.sh vX.Y.Z
#
# It reads the checked-in release_artifacts.toml manifest and FAILS if:
#   - any declared artifact is malformed, missing, or version-skewed
#     (version_probe stdout's last line != expected version);
#   - a buildable deliverable directory (top-level dir with a package.json,
#     e.g. web/, vscode/) exists with NO manifest entry — this makes
#     "finished but never shipped" (the F1/F2 class) STRUCTURALLY IMPOSSIBLE;
#   - a manifest entry's dir no longer exists (stale entry — both-direction
#     ratchet, same discipline as the ci/allowlists gates);
#   - a `deferred` bead is malformed, or (where `br` exists) not open.
#
# Modes:
#   --self-test        mandatory canary: the engine must FAIL the skewed and
#                      unmanifested fixtures under scripts/guards/testdata/
#                      release_canary/ — run in CI forever
#   --dry-run          expected version taken from the VERSION file
#   vX.Y.Z | X.Y.Z     full pre-flight for that tag (DSR step 0); also asserts
#                      the VERSION file matches the tag
#
# DSR build adaptation (bd-rc-track-d-release-docs-3ho85.1): goreleaser is
# retired (AGENTS.md); release metadata and build lockstep are verified solely
# against release_artifacts.toml, VERSION, Makefile, and install.sh (zero
# dependency on .goreleaser.yaml).
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BEAD_ID_RE='^bd-[a-z0-9][a-z0-9._-]*$'
CANARY_DIR="$REPO_ROOT/scripts/guards/testdata/release_canary"

fail_count=0
say() { echo "release_preflight: $*"; }
flunk() { echo "release_preflight: FAIL: $*" >&2; fail_count=$((fail_count + 1)); }

# ---------------------------------------------------------------------------
# parse_manifest <file>
# Emits "name<TAB>key<TAB>value" lines for [artifact.<name>] blocks, handling
# `key = "value"` and `key = 'value'` (TOML basic/literal single-line strings).
parse_manifest() {
  awk '
    /^[[:space:]]*#/ { next }
    /^\[artifact\./ {
      name = $0
      sub(/^\[artifact\./, "", name); sub(/\][[:space:]]*$/, "", name)
      print name "\t__block__\t1"
      next
    }
    name != "" && /^[[:space:]]*[A-Za-z_]+[[:space:]]*=/ {
      line = $0
      key = line; sub(/[[:space:]]*=.*$/, "", key); gsub(/[[:space:]]/, "", key)
      val = line; sub(/^[^=]*=[[:space:]]*/, "", val)
      q = substr(val, 1, 1)
      sq = sprintf("%c", 39)
      if (q == sq || q == "\"") {
        val = substr(val, 2)
        sub(q ".*$", "", val)
      }
      print name "\t" key "\t" val
    }
  ' "$1"
}

manifest_get() { # manifest_lines name key
  printf '%s\n' "$1" | awk -F'\t' -v n="$2" -v k="$3" '$1==n && $2==k { print $3; exit }'
}

# ---------------------------------------------------------------------------
# run_preflight <root> <manifest-file> <expected-version>
# Returns non-zero if any check fails; appends messages via flunk().
run_preflight() {
  local root="$1" manifest="$2" expected="$3"
  local before_fail=$fail_count

  if [ ! -f "$manifest" ]; then
    flunk "manifest $manifest not found"
    return 1
  fi

  local lines names
  lines="$(parse_manifest "$manifest")"
  names="$(printf '%s\n' "$lines" | awk -F'\t' '$2=="__block__" { print $1 }')"
  if [ -z "$names" ]; then
    flunk "$manifest declares no [artifact.*] blocks"
    return 1
  fi

  local tmp
  tmp="$(mktemp -d)"

  # --- per-entry validation + version probes -------------------------------
  local name kind probe deferred dir
  while IFS= read -r name; do
    kind="$(manifest_get "$lines" "$name" kind)"
    probe="$(manifest_get "$lines" "$name" version_probe)"
    deferred="$(manifest_get "$lines" "$name" deferred)"
    dir="$(manifest_get "$lines" "$name" dir)"

    if [ -z "$kind" ]; then
      flunk "artifact '$name': missing kind"
    fi
    if [ -n "$probe" ] && [ -n "$deferred" ]; then
      flunk "artifact '$name': version_probe and deferred are mutually exclusive"
      continue
    fi
    if [ -z "$probe" ] && [ -z "$deferred" ]; then
      flunk "artifact '$name': needs exactly one of version_probe or deferred"
      continue
    fi

    if [ -n "$dir" ] && [ ! -d "$root/$dir" ]; then
      flunk "artifact '$name': dir '$dir' does not exist (STALE manifest entry — delete or fix it in this PR)"
      continue
    fi

    if [ -n "$deferred" ]; then
      if [ -z "$dir" ]; then
        flunk "artifact '$name': deferred entries must declare their deliverable dir"
      fi
      if ! [[ "$deferred" =~ $BEAD_ID_RE ]]; then
        flunk "artifact '$name': deferred value '$deferred' is not a bead ID (waiver protocol is bead-first)"
      elif command -v br >/dev/null 2>&1; then
        local status
        status="$(br show "$deferred" --json 2>/dev/null | tr -d ' \n' | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')"
        if [ -z "$status" ]; then
          flunk "artifact '$name': deferred bead '$deferred' not found"
        elif [ "$status" = "closed" ]; then
          flunk "artifact '$name': deferred bead '$deferred' is CLOSED but the artifact is still deferred — ship it (register a version_probe) or reopen the bead"
        fi
      fi
      say "artifact '$name': deferred to open bead $deferred (dir $dir/ present)"
      continue
    fi

    local out last rc
    out="$(cd "$root" && EXPECTED_VERSION="$expected" PREFLIGHT_TMP="$tmp" bash -c "$probe" 2>&1)"
    rc=$?
    last="$(printf '%s\n' "$out" | sed -n '$p')"
    if [ $rc -ne 0 ]; then
      flunk "artifact '$name': version_probe exited $rc:
$out"
    elif [ "$last" != "$expected" ]; then
      flunk "artifact '$name': VERSION SKEW — probe reported '$last', expected '$expected'"
    else
      say "artifact '$name': version $last OK"
    fi
  done <<< "$names"

  # --- unmanifested-deliverable scan (the F1/F2 structural check) ----------
  local d rel entry_found
  for d in "$root"/*/; do
    rel="$(basename "$d")"
    case "$rel" in
      third_party|node_modules|testdata) continue ;;
    esac
    [ -f "$d/package.json" ] || continue
    entry_found="$(printf '%s\n' "$lines" | awk -F'\t' -v v="$rel" '$2=="dir" && $3==v { print $1; exit }')"
    if [ -z "$entry_found" ]; then
      flunk "buildable deliverable '$rel/' (has package.json) has NO entry in release_artifacts.toml — finished-but-never-shipped is not allowed; add an [artifact.*] block (version_probe, or deferred with an open bead)"
    fi
  done

  rm -rf "$tmp"
  [ $fail_count -eq $before_fail ]
}

# ---------------------------------------------------------------------------
# DSR build lockstep asserts against Makefile + install.sh + release_artifacts.toml
# with zero dependency on .goreleaser.yaml (bd-rc-track-d-release-docs-3ho85.1).
check_dsr_build_lockstep() {
  local root="${1:-$REPO_ROOT}"
  if [ ! -f "$root/Makefile" ]; then
    flunk "Makefile missing at $root/Makefile"
    return 1
  fi
  if ! grep -q 'internal/cli\.Version=\$(VERSION)' "$root/Makefile"; then
    flunk 'Makefile no longer stamps internal/cli.Version=$(VERSION) — DSR builds and "ntm version" would drift from the tag'
  fi
  local os arch
  for os in darwin linux; do
    for arch in amd64 arm64; do
      if ! grep -qE "GOOS=${os}[[:space:]]+GOARCH=${arch}" "$root/Makefile"; then
        flunk "Makefile build-all missing DSR target GOOS=${os} GOARCH=${arch}"
      fi
    done
  done
  if [ ! -f "$root/install.sh" ] || ! grep -q 'BIN_NAME="ntm"' "$root/install.sh"; then
    flunk 'install.sh missing BIN_NAME="ntm" asset resolution contract'
  fi
}

check_tag_reality_audit() {
  local root="$1" tag="$2"
  if [ "${NTM_PREFLIGHT_SKIP_REALITY_AUDIT:-0}" = "1" ]; then
    say "skipping docs/reality/<tag> check (NTM_PREFLIGHT_SKIP_REALITY_AUDIT=1)"
    return 0
  fi
  local norm_tag="v${tag#v}"
  if [ ! -f "$root/docs/reality/audit-${norm_tag}.md" ] || [ ! -f "$root/docs/reality/checklist-${norm_tag}.md" ]; then
    flunk "missing fresh reality audit artifacts docs/reality/audit-${norm_tag}.md and docs/reality/checklist-${norm_tag}.md — run 'bash scripts/reality_audit.sh ${norm_tag}' before DSR release"
  fi
}

# ---------------------------------------------------------------------------
self_test() {
  say "--self-test: canaries must FIRE (a guard that cannot demonstrate a catch is a placebo)"
  local pre

  # Canary 1: skewed version must fail.
  pre=$fail_count
  if run_preflight "$CANARY_DIR/tree" "$CANARY_DIR/skewed_manifest.toml" "9.9.9" >/dev/null 2>&1; then
    echo "release_preflight: CANARY FAILURE — skewed-version fixture PASSED the pre-flight" >&2
    exit 1
  fi
  fail_count=$pre

  # Canary 2: buildable deliverable dir without a manifest entry must fail.
  pre=$fail_count
  if run_preflight "$CANARY_DIR/tree" "$CANARY_DIR/unmanifested_manifest.toml" "9.9.9" >/dev/null 2>&1; then
    echo "release_preflight: CANARY FAILURE — unmanifested-deliverable fixture PASSED the pre-flight" >&2
    exit 1
  fi
  fail_count=$pre

  # Canary 3: a correct fixture must pass (no false positives).
  pre=$fail_count
  if ! run_preflight "$CANARY_DIR/tree" "$CANARY_DIR/good_manifest.toml" "9.9.9" >/dev/null 2>&1; then
    echo "release_preflight: CANARY FAILURE — known-good fixture FAILED the pre-flight" >&2
    exit 1
  fi
  fail_count=$pre

  # Canary 4: DSR lockstep must pass WITHOUT .goreleaser.yaml and fail when Makefile loses Version stamp.
  local dsr_tmp
  dsr_tmp="$(mktemp -d)"
  cp "$REPO_ROOT/Makefile" "$REPO_ROOT/install.sh" "$dsr_tmp/"
  pre=$fail_count
  check_dsr_build_lockstep "$dsr_tmp" >/dev/null 2>&1
  if [ $fail_count -ne $pre ]; then
    rm -rf "$dsr_tmp"
    echo "release_preflight: CANARY FAILURE — check_dsr_build_lockstep failed when .goreleaser.yaml is absent" >&2
    exit 1
  fi
  sed 's/internal\/cli\.Version=/internal\/cli.Broken=/' "$REPO_ROOT/Makefile" > "$dsr_tmp/Makefile"
  pre=$fail_count
  check_dsr_build_lockstep "$dsr_tmp" >/dev/null 2>&1
  if [ $fail_count -eq $pre ]; then
    rm -rf "$dsr_tmp"
    echo "release_preflight: CANARY FAILURE — check_dsr_build_lockstep accepted Makefile missing Version stamp" >&2
    exit 1
  fi
  fail_count=$pre
  rm -rf "$dsr_tmp"

  say "--self-test OK (all canaries fired; known-good and goreleaser-free fixtures passed)"
  exit 0
}

# ---------------------------------------------------------------------------
usage() {
  echo "usage: scripts/release_preflight.sh --self-test | --dry-run | vX.Y.Z" >&2
  exit 2
}

[ $# -eq 1 ] || usage
mode="$1"
case "$mode" in
  --self-test)
    self_test
    ;;
  --dry-run)
    expected="$(tr -d ' \n' < "$REPO_ROOT/VERSION")"
    [ -n "$expected" ] || { flunk "VERSION file is empty"; }
    say "dry-run against VERSION file: $expected"
    ;;
  --*)
    usage
    ;;
  *)
    expected="${mode#v}"
    is_tag_mode=1
    say "full pre-flight for tag $mode (expected version $expected)"
    file_version="$(tr -d ' \n' < "$REPO_ROOT/VERSION")"
    if [ "$file_version" != "$expected" ]; then
      flunk "VERSION file says '$file_version' but the release tag is '$expected' — artifact lockstep requires updating VERSION first"
    fi
    ;;
esac

check_dsr_build_lockstep "$REPO_ROOT"
if [ "${is_tag_mode:-0}" -eq 1 ]; then
  check_tag_reality_audit "$REPO_ROOT" "$expected"
fi
run_preflight "$REPO_ROOT" "$REPO_ROOT/release_artifacts.toml" "$expected" || true

if [ $fail_count -gt 0 ]; then
  echo "release_preflight.sh: FAILED ($fail_count problem(s)) — fix the artifact/manifest drift before releasing (DSR checklist step 0, AGENTS.md)" >&2
  exit 1
fi
say "OK — all declared artifacts present and version-locked to $expected"
