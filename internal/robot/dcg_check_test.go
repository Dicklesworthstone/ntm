package robot

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestGetDCGCheck_MissingCommand(t *testing.T) {
	out, err := GetDCGCheck("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Success {
		t.Fatalf("expected success=false for missing command")
	}
	if out.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected error_code=%s, got %s", ErrCodeInvalidFlag, out.ErrorCode)
	}
	if out.Allowed {
		t.Fatalf("expected allowed=false for missing command")
	}
}

func TestGetDCGCheck_MissingDCG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)

	out, err := GetDCGCheck("rm -rf /tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Success {
		t.Fatalf("expected success=false when dcg missing")
	}
	if out.ErrorCode != ErrCodeDependencyMissing {
		t.Fatalf("expected error_code=%s, got %s", ErrCodeDependencyMissing, out.ErrorCode)
	}
}

func TestGetDCGCheck_Allowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	out, err := GetDCGCheck("echo hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success=true")
	}
	if !out.Allowed {
		t.Fatalf("expected allowed=true")
	}
	if out.DCGVersion == "" {
		t.Fatalf("expected dcg_version to be set")
	}
	if out.BinaryPath == "" {
		t.Fatalf("expected binary_path to be set")
	}
}

func TestGetDCGCheck_Blocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	out, err := GetDCGCheck("rm -rf /tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success=true (check succeeded even if blocked)")
	}
	if out.Allowed {
		t.Fatalf("expected allowed=false")
	}
	if out.Reason == "" {
		t.Fatalf("expected reason to be set for blocked command")
	}
}

func TestGetDCGCheckWithOptions_Context(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	opts := DCGCheckOptions{
		Command: "rm -rf ./build",
		Context: "Cleaning build artifacts",
	}
	out, err := GetDCGCheckWithOptions(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success=true")
	}
	if out.Context != "Cleaning build artifacts" {
		t.Fatalf("expected context to be echoed, got %q", out.Context)
	}
}

func TestGetDCGCheckWithOptions_CWD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	opts := DCGCheckOptions{
		Command: "ls -la",
		CWD:     "/tmp",
	}
	out, err := GetDCGCheckWithOptions(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success=true")
	}
	if out.CWD != "/tmp" {
		t.Fatalf("expected cwd to be /tmp, got %q", out.CWD)
	}
}

func TestGetDCGCheckWithOptions_Severity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	// Blocked command should have severity
	opts := DCGCheckOptions{Command: "rm -rf /tmp"}
	out, err := GetDCGCheckWithOptions(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Allowed {
		t.Fatalf("expected allowed=false")
	}
	if out.Severity == "" {
		t.Fatalf("expected severity to be set for blocked command")
	}
}

func TestGetDCGCheckWithOptions_RuleMatched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	// Blocked command should have rule_matched
	opts := DCGCheckOptions{Command: "rm -rf /tmp"}
	out, err := GetDCGCheckWithOptions(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Allowed {
		t.Fatalf("expected allowed=false")
	}
	if out.RuleMatched == "" {
		t.Fatalf("expected rule_matched to be set for blocked command")
	}
}

func TestGetDCGCheckWithOptions_SafeSeverity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	// Allowed command should have severity=safe
	opts := DCGCheckOptions{Command: "echo hello"}
	out, err := GetDCGCheckWithOptions(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Allowed {
		t.Fatalf("expected allowed=true")
	}
	if out.Severity != "safe" {
		t.Fatalf("expected severity=safe for allowed command, got %q", out.Severity)
	}
}

func TestGetDCGCheckWithOptions_CombinedOptions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	opts := DCGCheckOptions{
		Command: "echo test",
		Context: "Testing combined options",
		CWD:     "/tmp/project",
	}
	out, err := GetDCGCheckWithOptions(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success=true")
	}
	if out.Context != "Testing combined options" {
		t.Fatalf("expected context echoed")
	}
	if out.CWD != "/tmp/project" {
		t.Fatalf("expected cwd echoed")
	}
}

func TestGetDCGCheckWithOptions_AgentHints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCGWithHints(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	// Blocked high-severity command should have agent hints
	opts := DCGCheckOptions{Command: "rm -rf /data"}
	out, err := GetDCGCheckWithOptions(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Allowed {
		t.Fatalf("expected allowed=false")
	}
	if out.AgentHints == nil {
		t.Fatalf("expected agent_hints to be set for high-severity blocked command")
	}
	if !out.AgentHints.RequiresConfirmation {
		t.Fatalf("expected requires_confirmation=true for high-severity command")
	}
}

func TestGetDCGCheck_TimestampIncluded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	out, err := GetDCGCheck("ls")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Timestamp == "" {
		t.Fatalf("expected timestamp to be set")
	}
}

func TestGetDCGCheck_CommandEchoed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	out, err := GetDCGCheck("echo hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Command != "echo hello world" {
		t.Fatalf("expected command to be echoed, got %q", out.Command)
	}
}

func TestGetDCGCheck_VersionIncluded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	tmpDir := t.TempDir()
	writeFakeDCG(t, tmpDir)
	t.Setenv("PATH", tmpDir)

	out, err := GetDCGCheck("ls")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.DCGVersion == "" {
		t.Fatalf("expected dcg_version to be set")
	}
}

func writeFakeDCGWithHints(t *testing.T, dir string) {
	t.Helper()

	dcgPath := filepath.Join(dir, "dcg")
	// Fake DCG script that includes safer_alternative in response
	script := `#!/bin/sh
set -eu

if [ "${1:-}" = "--version" ]; then
  echo "dcg 1.2.3"
  exit 0
fi

if [ "${1:-}" = "check" ]; then
  shift
  # Parse flags and find the command (last argument)
  cmd=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --json)
        shift
        ;;
      --context)
        shift 2
        ;;
      --cwd)
        shift 2
        ;;
      *)
        cmd="$1"
        shift
        ;;
    esac
  done

  case "$cmd" in
    *"rm -rf"*)
      echo "{\"command\":\"$cmd\",\"reason\":\"Destructive recursive delete\",\"severity\":\"high\",\"rule_matched\":\"RECURSIVE_DELETE\",\"safer_alternative\":\"trash-put $cmd\"}"
      exit 1
      ;;
  esac
  exit 0
fi

if [ "${1:-}" = "status" ]; then
  echo "{\"enabled\":true}"
  exit 0
fi

exit 0
`

	if err := os.WriteFile(dcgPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake dcg with hints: %v", err)
	}
}

func writeFakeDCG(t *testing.T, dir string) {
	t.Helper()

	dcgPath := filepath.Join(dir, "dcg")
	// Fake DCG script that handles --version, check (with --json, --context, --cwd), and status
	script := `#!/bin/sh
set -eu

if [ "${1:-}" = "--version" ]; then
  echo "dcg 1.2.3"
  exit 0
fi

if [ "${1:-}" = "check" ]; then
  shift
  # Parse flags and find the command (last argument)
  cmd=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --json)
        shift
        ;;
      --context)
        shift 2
        ;;
      --cwd)
        shift 2
        ;;
      *)
        cmd="$1"
        shift
        ;;
    esac
  done

  case "$cmd" in
    *"rm -rf"*)
      echo "{\"command\":\"$cmd\",\"reason\":\"blocked by fake policy\",\"severity\":\"high\",\"rule_matched\":\"RECURSIVE_DELETE\"}"
      exit 1
      ;;
  esac
  exit 0
fi

if [ "${1:-}" = "status" ]; then
  echo "{\"enabled\":true}"
  exit 0
fi

exit 0
`

	if err := os.WriteFile(dcgPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake dcg: %v", err)
	}
}
