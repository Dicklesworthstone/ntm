#!/usr/bin/env bash
# Aggregate JSONL logs from shell and Go E2E test runs.
# Schema reference: scripts/e2e/lib/log_schema.json

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_SHELL_DIR="${E2E_LOG_DIR:-${SCRIPT_DIR}/logs}"
DEFAULT_GO_DIR="${E2E_GO_LOG_DIR:-}" 

SHELL_DIR="$DEFAULT_SHELL_DIR"
GO_DIR="$DEFAULT_GO_DIR"
OUT_FILE="${SCRIPT_DIR}/logs/aggregate_$(date +%Y%m%d_%H%M%S).jsonl"
SORT=1
SINCE=""

usage() {
  cat <<EOF_USAGE
Usage: $(basename "$0") [options]

Options:
  --shell-dir DIR   Directory with shell JSONL logs (default: ${DEFAULT_SHELL_DIR})
  --go-dir DIR      Directory with Go JSONL logs (default: E2E_GO_LOG_DIR)
  --out FILE        Output JSONL file (default: ${OUT_FILE})
  --since RFC3339   Only include entries at/after timestamp
  --no-sort         Do not sort by timestamp (concatenate only)
  -h, --help        Show this help
EOF_USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --shell-dir)
      SHELL_DIR="$2"
      shift 2
      ;;
    --go-dir)
      GO_DIR="$2"
      shift 2
      ;;
    --out)
      OUT_FILE="$2"
      shift 2
      ;;
    --since)
      SINCE="$2"
      shift 2
      ;;
    --no-sort)
      SORT=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

files=()
if [[ -d "$SHELL_DIR" ]]; then
  while IFS= read -r -d '' f; do
    files+=("$f")
  done < <(find "$SHELL_DIR" -type f -name "*.jsonl" -print0)
fi

if [[ -n "$GO_DIR" && -d "$GO_DIR" ]]; then
  while IFS= read -r -d '' f; do
    files+=("$f")
  done < <(find "$GO_DIR" -type f -name "*.jsonl" -print0)
fi

if [[ ${#files[@]} -eq 0 ]]; then
  echo "No JSONL logs found. Provide --shell-dir or --go-dir." >&2
  exit 1
fi

mkdir -p "$(dirname "$OUT_FILE")"

if [[ $SORT -eq 0 ]]; then
  cat "${files[@]}" > "$OUT_FILE"
else
  if [[ -n "$SINCE" ]]; then
    jq -s --arg since "$SINCE" 'map(select(type=="object")) | map(select(.timestamp != null)) | map(select((.timestamp | fromdateiso8601) >= ($since | fromdateiso8601))) | sort_by(.timestamp) | .[]' "${files[@]}" > "$OUT_FILE"
  else
    jq -s 'map(select(type=="object")) | sort_by(.timestamp) | .[]' "${files[@]}" > "$OUT_FILE"
  fi
fi

echo "Aggregated ${#files[@]} log files -> $OUT_FILE"
