package robot

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGetCASSStatus_CapabilitiesAndHealthFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub cass uses POSIX sh")
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
set -eu
case "${1:-}" in
  status)
    echo '{"healthy":false,"conversations":4,"messages":18,"index":{"exists":true,"healthy":true,"documents":18},"database":{"exists":true,"opened":true,"healthy":true}}'
    ;;
  health)
    echo '{"healthy":true,"index":{"exists":true,"healthy":true},"database":{"exists":true,"opened":true,"healthy":true}}'
    ;;
  capabilities)
    echo '{"crate_version":"0.11.0","api_version":1,"features":["aggregations","timeline"],"connectors":["claude_code","codex","unknown_agent"]}'
    ;;
  *)
    echo '{}'
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "cass"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub cass: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := GetCASSStatus()
	if err != nil {
		t.Fatalf("GetCASSStatus: %v", err)
	}
	if !out.Success || !out.CASSAvailable || !out.Healthy {
		t.Fatalf("expected healthy CASS status via health fallback, got %+v", out)
	}
	if !out.SupportsAggregation || !out.SupportsTimeline {
		t.Fatalf("expected aggregation+timeline capabilities, got %+v", out)
	}
	if len(out.ActiveAgentConnectors) != 2 || out.ActiveAgentConnectors[0] != "claude_code" || out.ActiveAgentConnectors[1] != "codex" {
		t.Fatalf("active connectors = %v, want [claude_code codex]", out.ActiveAgentConnectors)
	}
}

func TestGetCASSContext_WorkspaceFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub cass uses POSIX sh")
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
set -eu
case "$*" in
  *"--workspace="*)
    echo '{"query":"auth","count":0,"total_matches":0,"hits":[]}'
    ;;
  *)
    echo '{"query":"auth","count":1,"total_matches":1,"hits":[{"source_path":"/sessions/s1.jsonl","agent":"claude_code","title":"JWT auth fix","score":0.95}]}'
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "cass"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub cass: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := GetCASSContext("auth")
	if err != nil {
		t.Fatalf("GetCASSContext: %v", err)
	}
	if !out.Success || len(out.RelevantSessions) != 1 || out.RelevantSessions[0].Summary != "JWT auth fix" {
		t.Fatalf("unexpected context output after workspace fallback: %+v", out)
	}
}
