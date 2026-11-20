#!/usr/bin/env bash
# web_and_surface_gate.sh — WS0-G8 Web Cockpit, VSCode, OpenAPI version, and
# DSR workflow lockstep gate (bd-rc2-track-5-release-dsr-00tdd.2).
#
# Bare-ubuntu safe (python3 stdlib only; no Go, Node, or br toolchain required).
# Enforces:
#   1. web/package.json declares engines.node >= 22 and matches VERSION
#   2. vscode/package.json matches VERSION, covers all contributes.commands in
#      activationEvents, and vscode/src/extension.ts clears intervals on disposal
#   3. docs/openapi.json info.version matches VERSION (never "dev")
#   4. Every web/src/app/**/page.tsx route exists in internal/webui/dist/ and
#      internal/webui/dist/index.html stamps data-ntm-version="<VERSION>"
#   5. Stale Next.js buildId directories in internal/webui/dist/_next/static/
#      are ratcheted in both directions via ci/allowlists/webui_dist.txt
#   6. Go version in go.mod matches GO_VERSION in .github/workflows/{ci,release,e2e-tests}.yml
#   7. All .github/workflows/*.yml remain workflow_dispatch-only (DSR-only policy)
#      with zero goreleaser action invocations
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1
# shellcheck source=lib/ratchet.sh
. "$REPO_ROOT/scripts/guards/lib/ratchet.sh"

ALLOWLIST="${WEB_SURFACE_ALLOWLIST:-ci/allowlists/webui_dist.txt}"
CANARY_DIR="scripts/guards/testdata/web_surface_canary"

if [ ! -f "$CANARY_DIR/README.txt" ]; then
  echo "web_and_surface_gate: CANARY FAILURE — missing $CANARY_DIR/README.txt" >&2
  exit 1
fi

scan_tree() {
  local root="$1"
  python3 - "$root" <<'PY'
import glob
import json
import os
import re
import sys

root = sys.argv[1]
violations = []

def read_text(rel):
    path = os.path.join(root, rel)
    if not os.path.isfile(path):
        return None
    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        return fh.read()

version_txt = read_text("VERSION")
if not version_txt or not version_txt.strip():
    violations.append("version:missing")
    expected_version = ""
else:
    expected_version = version_txt.strip()

# 1. web/package.json
web_pkg_raw = read_text("web/package.json")
if web_pkg_raw is None:
    violations.append("web_pkg:missing")
else:
    web_pkg = json.loads(web_pkg_raw)
    if expected_version and web_pkg.get("version") != expected_version:
        violations.append(f"web_pkg_version:{web_pkg.get('version') or 'missing'}")
    node_engine = ((web_pkg.get("engines") or {}).get("node") or "").strip()
    m = re.search(r"(\d+)", node_engine)
    if not node_engine or not m or int(m.group(1)) < 22:
        violations.append(f"web_engines_node:{node_engine or 'missing'}")

# 2. vscode/package.json + extension.ts timer cleanup
vs_pkg_raw = read_text("vscode/package.json")
if vs_pkg_raw is None:
    violations.append("vscode_pkg:missing")
else:
    vs_pkg = json.loads(vs_pkg_raw)
    if expected_version and vs_pkg.get("version") != expected_version:
        violations.append(f"vscode_pkg_version:{vs_pkg.get('version') or 'missing'}")
    act = set(vs_pkg.get("activationEvents") or [])
    if "*" not in act:
        cmds = ((vs_pkg.get("contributes") or {}).get("commands") or [])
        for c in cmds:
            cid = c.get("command")
            if cid and f"onCommand:{cid}" not in act:
                violations.append(f"vscode_activation_missing:{cid}")

vs_ext = read_text("vscode/src/extension.ts")
if vs_ext is not None:
    if "setInterval(" in vs_ext and "clearInterval(" not in vs_ext:
        violations.append("vscode_timer_cleanup:missing_clearInterval")

# 3. docs/openapi.json info.version lockstep
oa_raw = read_text("docs/openapi.json")
if oa_raw is None:
    violations.append("openapi:missing")
else:
    oa = json.loads(oa_raw)
    oa_ver = ((oa.get("info") or {}).get("version") or "").strip()
    if not oa_ver or oa_ver == "dev" or (expected_version and oa_ver != expected_version):
        violations.append(f"openapi_version:{oa_ver or 'missing'}")

# 4 & 5. Embedded webui routes + stale Next.js buildIds
dist_index = read_text("internal/webui/dist/index.html")
if dist_index is None:
    violations.append("webui_dist:missing_index_html")
elif expected_version and f'data-ntm-version="{expected_version}"' not in dist_index:
    violations.append("webui_dist:version_stamp_mismatch")

app_dir = os.path.join(root, "web", "src", "app")
if os.path.isdir(app_dir):
    for dirpath, _dirnames, filenames in os.walk(app_dir):
        if "page.tsx" not in filenames:
            continue
        rel = os.path.relpath(dirpath, app_dir)
        segs = []
        if rel != ".":
            for seg in rel.replace("\\", "/").split("/"):
                if seg.startswith("(") and seg.endswith(")"):
                    continue
                segs.append(seg)
        dist_html = os.path.join(root, "internal", "webui", "dist", *segs, "index.html")
        if not os.path.isfile(dist_html):
            route = "/" + "/".join(segs)
            violations.append(f"webui_route_missing:{route}")

static_dir = os.path.join(root, "internal", "webui", "dist", "_next", "static")
if os.path.isdir(static_dir):
    build_ids = []
    for entry in sorted(os.listdir(static_dir)):
        manifest = os.path.join(static_dir, entry, "_buildManifest.js")
        if os.path.isfile(manifest):
            build_ids.append(entry)
    index_txt = read_text("internal/webui/dist/index.txt") or ""
    active_ids = [bid for bid in build_ids if bid in index_txt]
    if not active_ids and build_ids:
        # Fallback when index.txt is absent: newest mtime directory is active.
        newest = max(build_ids, key=lambda b: os.path.getmtime(os.path.join(static_dir, b, "_buildManifest.js")))
        active_ids = [newest]
    active_set = set(active_ids)
    for bid in build_ids:
        if bid not in active_set:
            violations.append(f"stale_build_id:{bid}")

# 6. Go version lockstep across go.mod and workflows
gomod = read_text("go.mod") or ""
m_go = re.search(r"^go\s+([0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*$", gomod, re.M)
go_mod_ver = m_go.group(1) if m_go else ""

# 7. DSR-only workflow hygiene (workflow_dispatch-only + zero goreleaser + Go lockstep)
wf_dir = os.path.join(root, ".github", "workflows")
if os.path.isdir(wf_dir):
    for wf_path in sorted(glob.glob(os.path.join(wf_dir, "*.yml")) + glob.glob(os.path.join(wf_dir, "*.yaml"))):
        wf_name = os.path.basename(wf_path)
        with open(wf_path, "r", encoding="utf-8", errors="replace") as fh:
            text = fh.read()
        if "goreleaser/goreleaser-action" in text:
            violations.append(f"workflow_goreleaser:{wf_name}")
        # Inspect top-level `on:` block until next top-level key
        in_on = False
        for raw_line in text.splitlines():
            stripped = raw_line.strip()
            if not stripped or stripped.startswith("#"):
                continue
            if re.match(r"^on:\s*$", raw_line):
                in_on = True
                continue
            if re.match(r"^on:\s+\S+", raw_line):
                if "workflow_dispatch" not in raw_line:
                    violations.append(f"workflow_auto_trigger:{wf_name}")
                in_on = False
                continue
            if in_on:
                if not raw_line[0].isspace():
                    in_on = False
                else:
                    m_trig = re.match(r"^  ([a-z_]+):", raw_line)
                    if m_trig and m_trig.group(1) != "workflow_dispatch":
                        violations.append(f"workflow_auto_trigger:{wf_name}:{m_trig.group(1)}")
        if go_mod_ver:
            for m_wf_go in re.finditer(r'^\s*GO_VERSION:\s*["\']?([0-9]+\.[0-9]+(?:\.[0-9]+)?)["\']?', text, re.M):
                if m_wf_go.group(1) != go_mod_ver:
                    violations.append(f"workflow_go_version_skew:{wf_name}:{m_wf_go.group(1)}!={go_mod_ver}")

for v in sorted(set(violations)):
    print(v)
PY
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- mandatory firing canary ------------------------------------------------
mkdir -p "$TMP/canary/web/src/app/demo" \
         "$TMP/canary/vscode/src" \
         "$TMP/canary/docs" \
         "$TMP/canary/internal/webui/dist/_next/static/active123" \
         "$TMP/canary/internal/webui/dist/_next/static/stale999" \
         "$TMP/canary/.github/workflows"

printf '1.38.0\n' > "$TMP/canary/VERSION"
printf 'module example.com/ntm\n\ngo 1.26.10\n' > "$TMP/canary/go.mod"
cat > "$TMP/canary/web/package.json" <<'EOF'
{"name":"ntm-web","version":"1.37.0","engines":{"node":">=18.0.0"}}
EOF
printf 'export default function Demo(){return null;}\n' > "$TMP/canary/web/src/app/demo/page.tsx"
cat > "$TMP/canary/vscode/package.json" <<'EOF'
{"name":"ntm","version":"1.38.0","activationEvents":[],"contributes":{"commands":[{"command":"ntm.missing"}]}}
EOF
printf 'export function activate(){ setInterval(() => {}, 1000); }\n' > "$TMP/canary/vscode/src/extension.ts"
printf '{"openapi":"3.1.0","info":{"version":"dev"}}\n' > "$TMP/canary/docs/openapi.json"
printf '<html><body data-ntm-version="1.37.0"></body></html>\n' > "$TMP/canary/internal/webui/dist/index.html"
printf 'active123\n' > "$TMP/canary/internal/webui/dist/index.txt"
printf '/* active */\n' > "$TMP/canary/internal/webui/dist/_next/static/active123/_buildManifest.js"
printf '/* stale */\n' > "$TMP/canary/internal/webui/dist/_next/static/stale999/_buildManifest.js"
cat > "$TMP/canary/.github/workflows/ci.yml" <<'EOF'
name: CI
on:
  push:
    branches: [main]
env:
  GO_VERSION: "1.26.3"
jobs:
  bad:
    steps:
      - uses: goreleaser/goreleaser-action@v6
EOF

canary_out="$(scan_tree "$TMP/canary")"
for want in \
  '^web_pkg_version:1\.37\.0$' \
  '^web_engines_node:>=18\.0\.0$' \
  '^vscode_activation_missing:ntm\.missing$' \
  '^vscode_timer_cleanup:missing_clearInterval$' \
  '^openapi_version:dev$' \
  '^webui_dist:version_stamp_mismatch$' \
  '^webui_route_missing:/demo$' \
  '^stale_build_id:stale999$' \
  '^workflow_goreleaser:ci\.yml$' \
  '^workflow_auto_trigger:ci\.yml:push$' \
  '^workflow_go_version_skew:ci\.yml:1\.26\.3!=1\.26\.10$'; do
  if ! printf '%s\n' "$canary_out" | grep -qE "$want"; then
    echo "web_and_surface_gate: CANARY FAILURE — expected '$want' to fire" >&2
    printf '%s\n' "$canary_out" | sed 's/^/  got: /' >&2
    exit 1
  fi
done

# --- real tree check (ratcheted against ci/allowlists/webui_dist.txt) -------
live_out="$(scan_tree "$REPO_ROOT")"
awk '
  /^#[[:space:]]*permanent:/ { perm=1; next }
  /^#/ || /^[[:space:]]*$/   { next }
  perm && $2 == "permanent"  { next }
  { print $1 "\t" $2 }
' "$ALLOWLIST" > "$TMP/allowed_map.txt"

printf '%s\n' "$live_out" | awk 'NF' | LC_ALL=C sort -u > "$TMP/live_entries.txt"
awk -F'\t' '
  NR==FNR { bead[$1] = $2; next }
  { b = ($1 in bead) ? bead[$1] : "-"; print $1 "\t" b }
' "$TMP/allowed_map.txt" "$TMP/live_entries.txt" > "$TMP/live_pairs.txt"

if ! ratchet_compare "$ALLOWLIST" "$TMP/live_pairs.txt"; then
  echo "web_and_surface_gate: FAIL — Web/VSCode/OpenAPI/DSR-workflow lockstep or stale dist drift detected" >&2
  exit 1
fi

count="$(wc -l < "$TMP/live_entries.txt" | tr -d ' ')"
echo "web_and_surface_gate: OK (canaries fired; 0 new violations; ${count} allowlisted Rule-1 stale buildId(s))"
