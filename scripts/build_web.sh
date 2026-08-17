#!/usr/bin/env bash
# build_web.sh — rebuild the web dashboard static export and sync it into the
# go:embed tree (internal/webui/dist). Run after any change under web/ or a
# version bump; the internal/webui tests fail loudly when the embed is stale.
#
# F1b, bd-ws5-ship-or-cut-jv0rc.1.2.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$REPO_ROOT/web"
DIST_DIR="$REPO_ROOT/internal/webui/dist"

# npm is preferred: package-lock.json is the committed lockfile (npm ci is
# what the web-audit CI job runs); bun is a fallback for machines without npm.
if command -v npm >/dev/null 2>&1; then
  PKG_INSTALL=(npm ci)
  PKG_BUILD=(npm run build)
elif command -v bun >/dev/null 2>&1; then
  PKG_INSTALL=(bun install --no-save)
  PKG_BUILD=(bun run build)
else
  echo "build_web.sh: need npm or bun on PATH" >&2
  exit 1
fi

cd "$WEB_DIR"
"${PKG_INSTALL[@]}"
"${PKG_BUILD[@]}"

if [ ! -f "$WEB_DIR/out/index.html" ]; then
  echo "build_web.sh: next build did not produce out/index.html (output: 'export' misconfigured?)" >&2
  exit 1
fi

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"
cp -R "$WEB_DIR/out/." "$DIST_DIR/"

echo "build_web.sh: synced $(find "$DIST_DIR" -type f | wc -l | tr -d ' ') files into internal/webui/dist"
