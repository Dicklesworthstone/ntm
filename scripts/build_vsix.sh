#!/usr/bin/env bash
# build_vsix.sh — compile the VSCode extension and package it as a .vsix
# (F2, bd-ws5-ship-or-cut-jv0rc.2). Runs as the vscode-extension version_probe
# in release_artifacts.toml (release_preflight.sh --dry-run in CI + DSR
# checklist STEP 0) and during a DSR release to produce the attached asset
# vscode/dist/ntm-vscode-<version>.vsix.
#
# Marketplace publishing is DEFERRED (Q1 ledger entry on the bead): the vsix
# is a release asset only, installed via `code --install-extension`.
#
# Probe contract (release_preflight.sh): stdout's LAST line is the version
# baked into the packaged vsix; all tool chatter goes to stderr.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXT_DIR="$REPO_ROOT/vscode"
DIST_DIR="$EXT_DIR/dist"

for tool in npm npx node unzip; do
  command -v "$tool" >/dev/null 2>&1 || { echo "build_vsix.sh: need $tool on PATH" >&2; exit 1; }
done

expected="$(tr -d ' \n' < "$REPO_ROOT/VERSION")"
pkg_version="$(node -p "require('$EXT_DIR/package.json').version")"
if [ "$pkg_version" != "$expected" ]; then
  echo "build_vsix.sh: VERSION SKEW — vscode/package.json says '$pkg_version', VERSION says '$expected'; bump vscode/package.json (and refresh package-lock.json) in lockstep" >&2
  exit 1
fi

cd "$EXT_DIR"
npm ci --no-audit --no-fund >&2
npm run compile >&2 # tsc -p ./ — the same compile the ci.yml vscode-extension job gates

mkdir -p "$DIST_DIR"
vsix="$DIST_DIR/ntm-vscode-$pkg_version.vsix"
# vsce is a devDependency vendored in vscode/package-lock.json (bd-43ydf), so
# `npm ci` above is the only network step and the tool is integrity-pinned by
# the lockfile. --no-install makes any drift a hard failure instead of a
# silent registry fetch of a floating version at release time.
npx --no-install vsce package --out "$vsix" >&2

# Verify the packaged manifest really carries the expected version.
baked="$(unzip -p "$vsix" extension/package.json | node -p 'JSON.parse(require("fs").readFileSync(0, "utf8")).version')"
if [ "$baked" != "$expected" ]; then
  echo "build_vsix.sh: packaged vsix reports version '$baked', expected '$expected'" >&2
  exit 1
fi

echo "build_vsix.sh: built $vsix ($(du -h "$vsix" | cut -f1 | tr -d ' ')), version $baked" >&2
echo "$baked"
