#!/usr/bin/env bash
# gen_web_types.sh — Regenerate docs/openapi.json, docs/parity_matrix.json,
# and web/src/lib/api/schema.d.ts from the served Go router (bd-rc-track-e-uplift-xkt68.1).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

BIN_TMP="$(mktemp -t ntm-openapi-XXXXXX)"
trap 'rm -f "$BIN_TMP"' EXIT

go build -o "$BIN_TMP" ./cmd/ntm
"$BIN_TMP" openapi generate --output docs/openapi.json --parity-matrix docs/parity_matrix.json

(
  cd "$REPO_ROOT/web"
  PATH="/usr/local/bin:/usr/bin:$PATH" npm run gen:api
)

echo "gen_web_types.sh: OK (docs/openapi.json, docs/parity_matrix.json, web/src/lib/api/schema.d.ts synced)"
