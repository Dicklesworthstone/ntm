#!/usr/bin/env bash
# openapi_drift_gate.sh — WS0 OpenAPI 3.1 + parity_matrix + web schema.d.ts guard
# (bd-rc-track-e-uplift-xkt68.1).
#
# Bare-ubuntu safe (python3 stdlib only; no Go or npm toolchain required).
# Enforces:
#   1. servers[0].url == "http://127.0.0.1:7337" (`server_url:<got>`)
#   2. Exact route+method parity between docs/parity_matrix.json and docs/openapi.json (`parity_missing:*` / `parity_extra:*`)
#   3. >=98% typed response schemas across /api/v1/* operations (`typed_coverage:<pct>`)
#   4. SSE text/event-stream and WebSocket 101 Switching Protocols contracts present (`stream_contract:*`)
#   5. Every path in docs/openapi.json is emitted in web/src/lib/api/schema.d.ts (`web_schema_missing:<path>`)
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1
# shellcheck source=lib/ratchet.sh
. "$REPO_ROOT/scripts/guards/lib/ratchet.sh"

ALLOWLIST="${OPENAPI_DRIFT_ALLOWLIST:-ci/allowlists/openapi_drift.txt}"
CANARY_DIR="scripts/guards/testdata/openapi_drift_canary"

if [ ! -f "$CANARY_DIR/README.txt" ]; then
  echo "openapi_drift_gate: CANARY FAILURE — missing $CANARY_DIR/README.txt" >&2
  exit 1
fi

check_bundle() {
  local openapi_file="$1" parity_file="$2" schema_dts_file="$3"
  python3 - "$openapi_file" "$parity_file" "$schema_dts_file" <<'PY'
import json
import sys

openapi_path, parity_path, dts_path = sys.argv[1:4]
with open(openapi_path, "r", encoding="utf-8") as fh:
    spec = json.load(fh)
with open(parity_path, "r", encoding="utf-8") as fh:
    parity = json.load(fh)
with open(dts_path, "r", encoding="utf-8") as fh:
    dts = fh.read()

violations = []

servers = spec.get("servers") or []
server_url = servers[0].get("url", "") if servers else ""
if server_url != "http://127.0.0.1:7337":
    violations.append(f"server_url:{server_url or 'missing'}")

spec_ops = set()
v1_total = 0
v1_typed = 0
components = (spec.get("components") or {}).get("schemas") or {}

for path, item in sorted((spec.get("paths") or {}).items()):
    if f'"{path}":' not in dts:
        violations.append(f"web_schema_missing:{path}")
    for method in ("get", "post", "put", "patch", "delete"):
        op = item.get(method)
        if not op:
            continue
        spec_ops.add(f"{method.upper()}:{path}")
        if path.startswith("/api/v1/"):
            v1_total += 1
            responses = op.get("responses") or {}
            is_typed = False
            if "101" in responses:
                is_typed = True
            for code in ("200", "201", "202"):
                resp = responses.get(code)
                if not resp:
                    continue
                content = resp.get("content") or {}
                if "text/event-stream" in content or "application/gzip" in content:
                    is_typed = True
                app_json = content.get("application/json") or {}
                schema = app_json.get("schema") or {}
                ref = schema.get("$ref", "")
                if ref and ref != "#/components/schemas/SuccessEnvelope":
                    comp_name = ref.rsplit("/", 1)[-1]
                    if comp_name in components:
                        is_typed = True
            if is_typed:
                v1_typed += 1

parity_ops = set()
for ep in parity.get("endpoints") or []:
    m = (ep.get("method") or "").upper()
    p = ep.get("path") or ""
    if m and p:
        parity_ops.add(f"{m}:{p}")

for missing in sorted(parity_ops - spec_ops):
    violations.append(f"parity_missing:{missing}")
for extra in sorted(spec_ops - parity_ops):
    violations.append(f"parity_extra:{extra}")

ratio = (100.0 * v1_typed / v1_total) if v1_total else 0.0
if ratio < 98.0:
    violations.append(f"typed_coverage:{v1_typed}/{v1_total}({ratio:.2f}%)")

for sse_path in ("/events", "/api/v1/attention/stream"):
    sse_get = ((spec.get("paths") or {}).get(sse_path) or {}).get("get") or {}
    sse_200 = ((sse_get.get("responses") or {}).get("200") or {}).get("content") or {}
    if "text/event-stream" not in sse_200:
        violations.append(f"stream_contract:{sse_path}:missing_sse")

ws_get = ((spec.get("paths") or {}).get("/api/v1/ws") or {}).get("get") or {}
ws_resps = ws_get.get("responses") or {}
if "101" not in ws_resps:
    violations.append("stream_contract:/api/v1/ws:missing_101")

for v in sorted(set(violations)):
    print(v)
PY
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- mandatory canary -------------------------------------------------------
cat > "$TMP/bad_openapi.json" <<'EOF_OA'
{
  "openapi": "3.1.0",
  "servers": [{"url": "http://localhost:8080"}],
  "paths": {
    "/api/v1/foo": {
      "get": {
        "responses": {
          "200": {
            "content": {
              "application/json": {
                "schema": {"$ref": "#/components/schemas/SuccessEnvelope"}
              }
            }
          }
        }
      }
    }
  },
  "components": {"schemas": {"SuccessEnvelope": {"type": "object"}}}
}
EOF_OA
cat > "$TMP/bad_parity.json" <<'EOF_PM'
{
  "endpoints": [
    {"method": "GET", "path": "/api/v1/foo"},
    {"method": "POST", "path": "/api/v1/bar"}
  ]
}
EOF_PM
printf 'export interface paths {}\n' > "$TMP/bad_schema.d.ts"

canary_out="$(check_bundle "$TMP/bad_openapi.json" "$TMP/bad_parity.json" "$TMP/bad_schema.d.ts")"
for want in '^server_url:http://localhost:8080$' '^parity_missing:POST:/api/v1/bar$' '^typed_coverage:' '^web_schema_missing:/api/v1/foo$' '^stream_contract:/api/v1/attention/stream:missing_sse$' '^stream_contract:/api/v1/ws:missing_101$'; do
  if ! printf '%s\n' "$canary_out" | grep -qE "$want"; then
    echo "openapi_drift_gate: CANARY FAILURE — expected '$want' to fire" >&2
    printf '%s\n' "$canary_out" | sed 's/^/  got: /' >&2
    exit 1
  fi
done
printf '%s\n' "$canary_out" | LC_ALL=C sort -u | awk 'NF {print $0 "\t-"}' > "$TMP/canary_cur.txt"
if ratchet_compare "$ALLOWLIST" "$TMP/canary_cur.txt" '(server_url|parity_missing|parity_extra|typed_coverage|stream_contract|web_schema_missing):' >/dev/null 2>&1; then
  echo "openapi_drift_gate: CANARY FAILURE — ratchet_compare unexpectedly passed on canary violations" >&2
  exit 1
fi

# --- real repository scan + both-direction ratchet --------------------------
check_bundle "docs/openapi.json" "docs/parity_matrix.json" "web/src/lib/api/schema.d.ts" > "$TMP/raw_hits.txt"
LC_ALL=C sort -u "$TMP/raw_hits.txt" | awk 'NF {print $0 "\t-"}' > "$TMP/cur.txt"
ratchet_extract_pairs "$ALLOWLIST" '(server_url|parity_missing|parity_extra|typed_coverage|stream_contract|web_schema_missing):' > "$TMP/allowed.txt"
awk -F'\t' 'FILENAME == ARGV[1] { bead[$1]=$2; next } { e=$1; if (e in bead) print e "\t" bead[e]; else print e "\t-" }' \
  "$TMP/allowed.txt" "$TMP/cur.txt" | LC_ALL=C sort -u > "$TMP/joined.txt"

if ! ratchet_compare "$ALLOWLIST" "$TMP/joined.txt" '(server_url|parity_missing|parity_extra|typed_coverage|stream_contract|web_schema_missing):'; then
  echo "openapi_drift_gate.sh: FAILED — run 'bash scripts/gen_web_types.sh' to sync OpenAPI + parity_matrix + web schema.d.ts" >&2
  exit 1
fi

echo "openapi_drift_gate.sh: OK (canary fired; docs/openapi.json, docs/parity_matrix.json, web/src/lib/api/schema.d.ts in lockstep)"
