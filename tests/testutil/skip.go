package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/shirou/gopsutil/v4/cpu"
)

// CPUOverloadThreshold is the percentage above which a core is considered overloaded.
const CPUOverloadThreshold = 95.0

// SkipIfCPUOverloaded checks if all CPU cores are at 95%+ utilization and skips
// the test if so. This prevents flaky timing-based benchmark tests when the
// system is under extreme load.
//
// Use this at the start of any test that asserts on wall-clock time.
func SkipIfCPUOverloaded(t *testing.T) {
	t.Helper()

	// Sample CPU usage over 200ms per-core
	perCPU, err := cpu.Percent(200*time.Millisecond, true)
	if err != nil {
		// If we can't measure CPU, proceed with the test
		t.Logf("Warning: could not measure CPU load: %v", err)
		return
	}

	if len(perCPU) == 0 {
		return
	}

	// Check if ALL cores are at 95%+
	overloadedCores := 0
	for _, usage := range perCPU {
		if usage >= CPUOverloadThreshold {
			overloadedCores++
		}
	}

	if overloadedCores == len(perCPU) {
		t.Skipf("Skipping benchmark: system under extreme CPU load (all %d cores at %.0f%%+ utilization)",
			len(perCPU), CPUOverloadThreshold)
	}
}

// RequireTmux skips the test if tmux is not installed.
func RequireTmux(t *testing.T) {
	t.Helper()
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux not installed, skipping test")
	}
}

// RequireNTMBinary ensures tests run against a repo-built ntm binary.
//
// Many integration/E2E tests invoke "ntm" via PATH; relying on a globally installed
// binary is fragile (it may not match the workspace source). This helper builds the
// local binary once per test process and prepends it to PATH so LookPath/exec resolve
// the correct version.
func RequireNTMBinary(t *testing.T) {
	t.Helper()

	binary := BuildLocalNTM(t)
	binDir := filepath.Dir(binary)

	// Tests should never spawn detached long-running background processes.
	// This keeps `ntm spawn` from launching the internal monitor during `go test`.
	t.Setenv("NTM_DISABLE_INTERNAL_MONITOR", "1")

	existing := os.Getenv("PATH")
	sep := string(os.PathListSeparator)
	if existing == "" {
		t.Setenv("PATH", binDir)
		return
	}
	if existing == binDir || strings.HasPrefix(existing, binDir+sep) {
		return
	}
	t.Setenv("PATH", binDir+sep+existing)
}

// RequireTmuxServer skips the test if no tmux server is running.
// Some tests need a tmux server already running.
func RequireTmuxServer(t *testing.T) {
	t.Helper()
	RequireTmux(t)
	if err := exec.Command(tmux.BinaryPath(), "list-sessions").Run(); err != nil {
		// Start a temporary server
		t.Log("No tmux server running, will create one for test")
	}
}

// RequireNotCI skips the test when running in CI environments.
// Useful for tests that require interactive terminal features.
func RequireNotCI(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
		t.Skip("skipping test in CI environment")
	}
}

// RequireCI only runs the test in CI environments.
func RequireCI(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" && os.Getenv("GITHUB_ACTIONS") == "" {
		t.Skip("test only runs in CI environment")
	}
}

// RequireRoot skips the test if not running as root.
func RequireRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("test requires root privileges")
	}
}

// RequireEnv skips the test if the specified environment variable is not set.
func RequireEnv(t *testing.T, envVar string) {
	t.Helper()
	if os.Getenv(envVar) == "" {
		t.Skipf("environment variable %s not set, skipping test", envVar)
	}
}

// RequireLinux skips the test on non-Linux systems.
func RequireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("test requires Linux, running on %s", runtime.GOOS)
	}
}

// RequireMacOS skips the test on non-macOS systems.
func RequireMacOS(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skipf("test requires macOS, running on %s", runtime.GOOS)
	}
}

// RequireUnix skips the test on non-Unix systems (Windows).
func RequireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test requires Unix-like system")
	}
}

// RequireIntegration skips the test unless integration tests are enabled.
// Set NTM_INTEGRATION_TESTS=1 to run integration tests.
func RequireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("NTM_INTEGRATION_TESTS") == "" {
		t.Skip("integration tests disabled, set NTM_INTEGRATION_TESTS=1 to enable")
	}
}

// RequireE2E skips the test unless E2E tests are enabled.
// Set NTM_E2E_TESTS=1 to run E2E tests.
func RequireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("NTM_E2E_TESTS") == "" {
		t.Skip("E2E tests disabled, set NTM_E2E_TESTS=1 to enable")
	}
}

// SkipShort skips the test if -short flag is passed.
func SkipShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
}

// IntegrationTestPrecheck runs all common prechecks for integration tests.
// This is a convenience function that combines common skip conditions.
func IntegrationTestPrecheck(t *testing.T) {
	t.Helper()
	RequireIntegration(t)
	RequireTmux(t)
	RequireNTMBinary(t)
}

// E2ETestPrecheck runs all common prechecks for E2E tests.
func E2ETestPrecheck(t *testing.T) {
	t.Helper()
	RequireE2E(t)
	RequireTmux(t)
	RequireNTMBinary(t)
}

// SkipIfBdUnavailable skips the test if bd (beads_rust) is not installed.
func SkipIfBdUnavailable(t *testing.T) {
	t.Helper()
	if err := exec.Command("br", "--version").Run(); err != nil {
		t.Skip("br (beads_rust) not installed, skipping test")
	}
}

// SkipIfBvUnavailable skips the test if bv is not installed.
func SkipIfBvUnavailable(t *testing.T) {
	t.Helper()
	if err := exec.Command("bv", "--version").Run(); err != nil {
		t.Skip("bv not installed, skipping test")
	}
}

// SkipIfMailUnavailable skips the test if Agent Mail MCP server is not available.
func SkipIfMailUnavailable(t *testing.T) {
	t.Helper()
	// Agent Mail requires MCP server running - check for socket or command
	// For now, skip based on environment variable
	if os.Getenv("NTM_MAIL_TESTS") == "" {
		t.Skip("Agent Mail tests disabled, set NTM_MAIL_TESTS=1 to enable")
	}
}

// SkipIfUBSUnavailable skips the test if UBS scanner is not installed.
func SkipIfUBSUnavailable(t *testing.T) {
	t.Helper()
	if err := exec.Command("ubs", "--version").Run(); err != nil {
		t.Skip("ubs (Ultimate Bug Scanner) not installed, skipping test")
	}
}
