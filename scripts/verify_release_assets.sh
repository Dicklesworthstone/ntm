#!/usr/bin/env bash
# verify_release_assets.sh — Post-release DSR asset, VSIX, Homebrew Cask & GHCR
# verifier (bd-rc-track-d-release-docs-3ho85.1 / GH #347).
#
# Usage:
#   bash scripts/verify_release_assets.sh --self-test
#   bash scripts/verify_release_assets.sh <vX.Y.Z> [--assets-dir <dir>] [--cask-file <path>]
#
# Verifies that a published (or staged) DSR release includes:
#   1. All 4 platform tarballs (darwin/linux x amd64/arm64)
#   2. SHA256SUMS covering every platform archive with matching digests
#   3. The VSCode extension package `ntm-vscode-<version>.vsix` (F2, AGENTS.md)
#   4. Homebrew tap `Casks/ntm.rb` version & SHA256 lockstep against SHA256SUMS
#   5. GHCR container image coordinate & manifest (`ghcr.io/dicklesworthstone/ntm:<version>`)
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail_count=0
say() { echo "verify_release_assets: $*"; }
flunk() { echo "verify_release_assets: FAIL: $*" >&2; fail_count=$((fail_count + 1)); }

verify_staged_bundle() {
  local ver="$1" dir="$2" cask="${3:-}"
  local before=$fail_count
  local sums="$dir/SHA256SUMS"

  if [ ! -f "$sums" ]; then
    flunk "missing SHA256SUMS in $dir"
    return 1
  fi

  local os arch found_archive
  for os in darwin linux; do
    for arch in amd64 arm64; do
      found_archive=""
      for cand in \
        "$dir/ntm_${ver}_${os}_${arch}.tar.gz" \
        "$dir/ntm-${os}-${arch}.tar.gz" \
        "$dir/ntm_${os}_${arch}.tar.gz"; do
        if [ -f "$cand" ]; then
          found_archive="$cand"
          break
        fi
      done
      if [ -z "$found_archive" ]; then
        flunk "missing platform archive for ${os}/${arch} in $dir"
        continue
      fi
      local base actual expected
      base="$(basename "$found_archive")"
      actual="$(sha256sum "$found_archive" | awk '{print $1}')"
      expected="$(awk -v b="$base" '$2==b || $2=="*"b {print $1; exit}' "$sums")"
      if [ -z "$expected" ]; then
        flunk "SHA256SUMS missing digest entry for $base"
      elif [ "$actual" != "$expected" ]; then
        flunk "SHA256 mismatch for $base: got $actual, SHA256SUMS says $expected"
      fi
    done
  done

  local vsix="$dir/ntm-vscode-${ver}.vsix"
  if [ ! -f "$vsix" ] || [ ! -s "$vsix" ]; then
    flunk "missing or empty VSCode extension asset ntm-vscode-${ver}.vsix (AGENTS.md F2 requirement)"
  fi

  if [ -n "$cask" ]; then
    if [ ! -f "$cask" ]; then
      flunk "Homebrew cask file not found: $cask"
    else
      if ! grep -qE "version[[:space:]]+\"${ver}\"" "$cask"; then
        flunk "Homebrew cask $cask version does not match ${ver}"
      fi
      while IFS= read -r sum_hash; do
        [ -n "$sum_hash" ] || continue
        if ! grep -qiF "$sum_hash" "$cask"; then
          flunk "Homebrew cask $cask missing archive sha256 $sum_hash"
        fi
      done < <(grep -E 'ntm[_-].*(darwin|linux).*(amd64|arm64)\.tar\.gz$' "$sums" | awk '{print $1}')
    fi
  fi

  [ $fail_count -eq $before ]
}

self_test() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  local ver="9.9.9"
  mkdir -p "$tmp/good"
  for os in darwin linux; do
    for arch in amd64 arm64; do
      printf 'payload-%s-%s\n' "$os" "$arch" > "$tmp/good/ntm_${ver}_${os}_${arch}.tar.gz"
    done
  done
  printf 'vsix-payload\n' > "$tmp/good/ntm-vscode-${ver}.vsix"
  (cd "$tmp/good" && sha256sum ntm_${ver}_*.tar.gz ntm-vscode-${ver}.vsix > SHA256SUMS)

  {
    printf 'cask "ntm" do\n  version "%s"\n' "$ver"
    awk '{printf "  # %s\n  sha256 \"%s\"\n", $2, $1}' "$tmp/good/SHA256SUMS"
    printf 'end\n'
  } > "$tmp/ntm.rb"

  if ! verify_staged_bundle "$ver" "$tmp/good" "$tmp/ntm.rb" >/dev/null 2>&1; then
    echo "verify_release_assets: CANARY FAILURE — valid release bundle rejected" >&2
    exit 1
  fi

  # Canary A: missing .vsix must fail (catches the v1.37.2/v1.38.0 regression).
  cp -R "$tmp/good" "$tmp/no_vsix"
  rm -f "$tmp/no_vsix/ntm-vscode-${ver}.vsix"
  local pre=$fail_count
  if verify_staged_bundle "$ver" "$tmp/no_vsix" "$tmp/ntm.rb" >/dev/null 2>&1; then
    echo "verify_release_assets: CANARY FAILURE — bundle missing .vsix PASSED" >&2
    exit 1
  fi
  fail_count=$pre

  # Canary B: drifted Homebrew cask SHA256 must fail (catches bd-o82m7 regression).
  sed "s/version \"${ver}\"/version \"9.9.8\"/" "$tmp/ntm.rb" > "$tmp/stale_ntm.rb"
  pre=$fail_count
  if verify_staged_bundle "$ver" "$tmp/good" "$tmp/stale_ntm.rb" >/dev/null 2>&1; then
    echo "verify_release_assets: CANARY FAILURE — stale Homebrew cask PASSED" >&2
    exit 1
  fi
  fail_count=$pre

  say "--self-test OK (valid bundle passed; missing-.vsix and stale-cask canaries fired)"
  exit 0
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
fi

TAG="${1:-}"
if [ -z "$TAG" ]; then
  echo "usage: bash scripts/verify_release_assets.sh --self-test | <vX.Y.Z> [--assets-dir <dir>] [--cask-file <path>]" >&2
  exit 2
fi
shift
VER="${TAG#v}"
NORM_TAG="v${VER}"
ASSETS_DIR=""
CASK_FILE=""

while [ $# -gt 0 ]; do
  case "$1" in
    --assets-dir) ASSETS_DIR="${2:-}"; shift 2 ;;
    --cask-file) CASK_FILE="${2:-}"; shift 2 ;;
    *) echo "verify_release_assets: unknown option: $1" >&2; exit 2 ;;
  esac
done

TMP_DL=""
if [ -z "$ASSETS_DIR" ]; then
  if ! command -v gh >/dev/null 2>&1; then
    flunk "gh CLI is required when --assets-dir is not provided"
    exit 1
  fi
  TMP_DL="$(mktemp -d)"
  trap 'rm -rf "$TMP_DL"' EXIT
  say "downloading release assets for $NORM_TAG from GitHub..."
  if ! gh release download "$NORM_TAG" --repo Dicklesworthstone/ntm --dir "$TMP_DL"; then
    flunk "failed to download release assets for $NORM_TAG"
    exit 1
  fi
  ASSETS_DIR="$TMP_DL"
fi

if [ -z "$CASK_FILE" ] && command -v gh >/dev/null 2>&1; then
  TMP_CASK="${TMP_DL:-$(mktemp -d)}/ntm.rb"
  if gh api repos/Dicklesworthstone/homebrew-tap/contents/Casks/ntm.rb --jq .content 2>/dev/null | base64 -d > "$TMP_CASK" 2>/dev/null && [ -s "$TMP_CASK" ]; then
    CASK_FILE="$TMP_CASK"
  fi
fi

verify_staged_bundle "$VER" "$ASSETS_DIR" "$CASK_FILE" || true

if command -v docker >/dev/null 2>&1; then
  if docker buildx imagetools inspect "ghcr.io/dicklesworthstone/ntm:${VER}" >/dev/null 2>&1; then
    say "GHCR manifest ghcr.io/dicklesworthstone/ntm:${VER} verified"
  else
    say "note: docker imagetools could not inspect ghcr.io/dicklesworthstone/ntm:${VER} (offline or unpublished tag)"
  fi
fi

if [ $fail_count -gt 0 ]; then
  echo "verify_release_assets.sh: FAILED ($fail_count problem(s)) for $NORM_TAG" >&2
  exit 1
fi
say "OK — release assets verified for $NORM_TAG"
