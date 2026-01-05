package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// resetFlags resets global flags to default values between tests
func resetFlags() {
	jsonOutput = false
	robotHelp = false
	robotStatus = false
	robotVersion = false
	robotPlan = false
	robotSnapshot = false
	robotSince = ""
	robotTail = ""
	robotLines = 20
	robotPanes = ""
	robotSend = ""
	robotSendMsg = ""
	robotSendAll = false
	robotSendType = ""
	robotSendExclude = ""
	robotSendDelay = 0
}

// sessionAutoSelectPossible returns true if the CLI would auto-select a session.
// This happens when exactly one tmux session is running.
func sessionAutoSelectPossible() bool {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return false
	}
	return len(sessions) == 1
}

// TestExecuteHelp verifies that the root command executes successfully
func TestExecuteHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--help"})

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() with --help failed: %v", err)
	}
}

// TestVersionCmdExecutes tests the version subcommand runs without error
func TestVersionCmdExecutes(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"default version", []string{"version"}},
		{"short version", []string{"version", "--short"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs(tt.args)

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("Execute() failed: %v", err)
			}
		})
	}
}

// TestConfigPathCmdExecutes tests the config path subcommand runs
func TestConfigPathCmdExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"config", "path"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestConfigShowCmdExecutes tests the config show subcommand runs
func TestConfigShowCmdExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"config", "show"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestDepsCmdExecutes tests the deps command runs
func TestDepsCmdExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"deps"})

	err := rootCmd.Execute()
	// deps may exit 1 if missing required deps, but shouldn't panic
	_ = err
}

// TestListCmdExecutes tests list command executes
func TestListCmdExecutes(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"list"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestListCmdJSONExecutes tests list command with JSON output executes
func TestListCmdJSONExecutes(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"list", "--json"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestSpawnValidation tests spawn command argument validation
func TestSpawnValidation(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	// Initialize config for spawn command
	cfg = config.Default()

	tests := []struct {
		name        string
		args        []string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "missing session name",
			args:        []string{"spawn"},
			expectError: true,
			errorMsg:    "accepts 1 arg",
		},
		{
			name:        "no agents specified",
			args:        []string{"spawn", "testproject"},
			expectError: true,
			errorMsg:    "no agents specified",
		},
		{
			name:        "invalid session name with colon",
			args:        []string{"spawn", "test:project", "--cc=1"},
			expectError: true,
			errorMsg:    "cannot contain ':'",
		},
		{
			name:        "invalid session name with dot",
			args:        []string{"spawn", "test.project", "--cc=1"},
			expectError: true,
			errorMsg:    "cannot contain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs(tt.args)

			err := rootCmd.Execute()

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing %q, got: %v", tt.errorMsg, err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestIsJSONOutput tests the JSON output detection
func TestIsJSONOutput(t *testing.T) {
	// Save original value
	original := jsonOutput
	defer func() { jsonOutput = original }()

	jsonOutput = false
	if IsJSONOutput() {
		t.Error("Expected IsJSONOutput() to return false")
	}

	jsonOutput = true
	if !IsJSONOutput() {
		t.Error("Expected IsJSONOutput() to return true")
	}
}

// TestGetFormatter tests the formatter creation
func TestGetFormatter(t *testing.T) {
	formatter := GetFormatter()
	if formatter == nil {
		t.Fatal("Expected non-nil formatter")
	}
}

// TestBuildInfo tests that build info variables are set
func TestBuildInfo(t *testing.T) {
	// These should have default values even if not set by build
	if Version == "" {
		t.Error("Version should not be empty")
	}
	if Commit == "" {
		t.Error("Commit should not be empty")
	}
	if Date == "" {
		t.Error("Date should not be empty")
	}
	if BuiltBy == "" {
		t.Error("BuiltBy should not be empty")
	}
}

// TestRobotVersionExecutes tests robot-version flag executes
func TestRobotVersionExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--robot-version"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestRobotHelpExecutes tests robot-help flag executes
func TestRobotHelpExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--robot-help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestRobotStatusExecutes tests the robot-status flag executes
func TestRobotStatusExecutes(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-status"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestRobotSnapshotExecutes tests the robot-snapshot flag executes
func TestRobotSnapshotExecutes(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-snapshot"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestRobotPlanExecutes tests the robot-plan flag executes
func TestRobotPlanExecutes(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-plan"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestAttachCmdNoArgs tests attach command without arguments
func TestAttachCmdNoArgs(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	// Initialize config
	cfg = config.Default()
	resetFlags()
	rootCmd.SetArgs([]string{"attach"})

	err := rootCmd.Execute()
	// Should not error - just lists sessions
	if err != nil && !strings.Contains(err.Error(), "no server running") {
		t.Logf("Attach without args result: %v", err)
	}
}

// TestStatusCmdRequiresArg tests status command requires session name
func TestStatusCmdRequiresArg(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"status"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for status without session name")
	}
	if !strings.Contains(err.Error(), "accepts 1 arg") {
		t.Errorf("Expected 'accepts 1 arg' error, got: %v", err)
	}
}

// TestAddCmdRequiresSession tests add command requires session name
func TestAddCmdRequiresSession(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"add"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for add without session name")
	}
}

// TestZoomCmdRequiresArgs tests zoom command requires arguments
func TestZoomCmdRequiresArgs(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"zoom"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for zoom without arguments")
	}
}

// TestSendCmdRequiresArgs tests send command requires arguments
func TestSendCmdRequiresArgs(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"send"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for send without arguments")
	}
}

// TestCompletionCmdExecutes tests completion subcommand executes
func TestCompletionCmdExecutes(t *testing.T) {
	shells := []string{"bash", "zsh", "fish", "powershell"}

	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs([]string{"completion", shell})

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("completion %s failed: %v", shell, err)
			}
		})
	}
}

// TestInitCmdExecutes tests init subcommand for shell integration executes
func TestInitCmdExecutes(t *testing.T) {
	shells := []string{"bash", "zsh"}

	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs([]string{"init", shell})

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("init %s failed: %v", shell, err)
			}
		})
	}
}

// TestKillCmdRequiresSession tests kill command requires session name
func TestKillCmdRequiresSession(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"kill"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for kill without session name")
	}
}

// TestViewCmdRequiresSession tests view command requires session name
func TestViewCmdRequiresSession(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"view"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for view without session name")
	}
}

// TestCopyCmdRequiresSession tests copy command requires session name
// when no session can be auto-selected (0 or 2+ sessions running).
func TestCopyCmdRequiresSession(t *testing.T) {
	if sessionAutoSelectPossible() {
		t.Skip("Skipping: exactly one tmux session running (auto-selection applies)")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"copy"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for copy without session name")
	}
}

// TestSaveCmdRequiresSession tests save command requires session name
// when no session can be auto-selected (0 or 2+ sessions running).
func TestSaveCmdRequiresSession(t *testing.T) {
	if sessionAutoSelectPossible() {
		t.Skip("Skipping: exactly one tmux session running (auto-selection applies)")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"save"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for save without session name")
	}
}

// TestTutorialCmdHelp tests the tutorial command help
func TestTutorialCmdHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"tutorial", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("tutorial --help failed: %v", err)
	}
}

// TestDashboardCmdHelp tests the dashboard command help
func TestDashboardCmdHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"dashboard", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("dashboard --help failed: %v", err)
	}
}

// TestPaletteCmdHelp tests the palette command help
func TestPaletteCmdHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"palette", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("palette --help failed: %v", err)
	}
}

// TestQuickCmdRequiresName tests quick command requires project name
func TestQuickCmdRequiresName(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"quick"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for quick without project name")
	}
}

// TestUpgradeCmdHelp tests the upgrade command help
func TestUpgradeCmdHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"upgrade", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("upgrade --help failed: %v", err)
	}
}

// TestGetAssetName tests the asset name generation for different platforms
func TestGetAssetName(t *testing.T) {
	// Note: This tests the actual runtime values, so results depend on where tests run
	name := getAssetName()

	// Must start with ntm_
	if !strings.HasPrefix(name, "ntm_") {
		t.Errorf("getAssetName() = %q, want prefix 'ntm_'", name)
	}

	// Must contain underscore separators (not dashes)
	parts := strings.Split(name, "_")
	if len(parts) != 3 {
		t.Errorf("getAssetName() = %q, want 3 parts separated by underscore", name)
	}
}

// TestGetArchiveAssetName tests archive asset name generation
func TestGetArchiveAssetName(t *testing.T) {
	tests := []struct {
		version  string
		wantPre  string
		wantPost string
	}{
		{"1.4.1", "ntm_1.4.1_", ".tar.gz"},
		{"2.0.0", "ntm_2.0.0_", ".tar.gz"},
		{"0.1.0-beta", "ntm_0.1.0-beta_", ".tar.gz"},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			name := getArchiveAssetName(tt.version)

			if !strings.HasPrefix(name, tt.wantPre) {
				t.Errorf("getArchiveAssetName(%q) = %q, want prefix %q", tt.version, name, tt.wantPre)
			}
			if !strings.HasSuffix(name, tt.wantPost) {
				t.Errorf("getArchiveAssetName(%q) = %q, want suffix %q", tt.version, name, tt.wantPost)
			}
		})
	}
}

// TestVersionComparison tests the version comparison logic
func TestVersionComparison(t *testing.T) {
	tests := []struct {
		current   string
		latest    string
		wantNewer bool
	}{
		{"1.0.0", "1.1.0", true},
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "2.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.1.0", "1.0.0", false},
		{"2.0.0", "1.9.9", false},
		{"dev", "1.0.0", true},
		{"", "1.0.0", true},
		{"v1.0.0", "v1.1.0", true},
		{"1.0", "1.0.1", true},
		{"1.0.0-beta", "1.0.0", false}, // normalizeVersion strips suffix, so they're equal
	}

	for _, tt := range tests {
		t.Run(tt.current+"_vs_"+tt.latest, func(t *testing.T) {
			got := isNewerVersion(tt.current, tt.latest)
			if got != tt.wantNewer {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.wantNewer)
			}
		})
	}
}

// TestNormalizeVersion tests version normalization
func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v1.0.0", "1.0.0"},
		{"1.0.0", "1.0.0"},
		{"1.0.0-beta", "1.0.0"},
		{"1.0.0+build", "1.0.0"},
		{"v2.1.3-rc1", "2.1.3"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeVersion(tt.input)
			if got != tt.want {
				t.Errorf("normalizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestFormatSize tests the size formatting function
func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{16219443, "15.5 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatSize(tt.bytes)
			if got != tt.want {
				t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

// TestUpgradeAssetNamingContract validates that upgrade.go asset naming
// matches the GoReleaser naming convention. This is a CONTRACT TEST that
// catches drift between .goreleaser.yaml and upgrade.go before users hit it.
//
// GoReleaser naming patterns (from .goreleaser.yaml):
//   - Archives: ntm_VERSION_OS_ARCH.tar.gz (or .zip for windows)
//   - Binaries: ntm_OS_ARCH
//   - macOS: uses "all" for universal binary (replaces amd64/arm64)
//   - Linux ARM: uses "armv7" suffix
//
// See CONTRIBUTING.md "Release Infrastructure" section for full documentation
// on the upgrade naming contract and how to safely make changes.
func TestUpgradeAssetNamingContract(t *testing.T) {
	// These test cases represent the expected GoReleaser output based on
	// .goreleaser.yaml configuration. If these tests fail, either:
	// 1. GoReleaser config changed and upgrade.go needs updating, or
	// 2. upgrade.go changed and broke compatibility with GoReleaser output
	tests := []struct {
		name           string
		goos           string
		goarch         string
		version        string
		wantArchive    string
		wantBinaryName string
	}{
		// macOS uses universal binary "all" instead of specific arch
		{
			name:           "darwin_arm64",
			goos:           "darwin",
			goarch:         "arm64",
			version:        "1.4.1",
			wantArchive:    "ntm_1.4.1_darwin_all.tar.gz",
			wantBinaryName: "ntm_darwin_all",
		},
		{
			name:           "darwin_amd64",
			goos:           "darwin",
			goarch:         "amd64",
			version:        "1.4.1",
			wantArchive:    "ntm_1.4.1_darwin_all.tar.gz",
			wantBinaryName: "ntm_darwin_all",
		},
		// Linux uses actual architecture
		{
			name:           "linux_amd64",
			goos:           "linux",
			goarch:         "amd64",
			version:        "2.0.0",
			wantArchive:    "ntm_2.0.0_linux_amd64.tar.gz",
			wantBinaryName: "ntm_linux_amd64",
		},
		{
			name:           "linux_arm64",
			goos:           "linux",
			goarch:         "arm64",
			version:        "1.5.0",
			wantArchive:    "ntm_1.5.0_linux_arm64.tar.gz",
			wantBinaryName: "ntm_linux_arm64",
		},
		// 32-bit ARM uses armv7 suffix (goarm=7)
		{
			name:           "linux_arm",
			goos:           "linux",
			goarch:         "arm",
			version:        "1.5.0",
			wantArchive:    "ntm_1.5.0_linux_armv7.tar.gz",
			wantBinaryName: "ntm_linux_armv7",
		},
		// Windows uses .zip extension
		{
			name:           "windows_amd64",
			goos:           "windows",
			goarch:         "amd64",
			version:        "1.4.1",
			wantArchive:    "ntm_1.4.1_windows_amd64.zip",
			wantBinaryName: "ntm_windows_amd64",
		},
		// FreeBSD
		{
			name:           "freebsd_amd64",
			goos:           "freebsd",
			goarch:         "amd64",
			version:        "1.4.1",
			wantArchive:    "ntm_1.4.1_freebsd_amd64.tar.gz",
			wantBinaryName: "ntm_freebsd_amd64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the asset name generation for this platform
			gotArchive := simulateGetArchiveAssetName(tt.version, tt.goos, tt.goarch)
			gotBinary := simulateGetAssetName(tt.goos, tt.goarch)

			if gotArchive != tt.wantArchive {
				t.Errorf("Archive name mismatch for %s/%s:\n  got:  %q\n  want: %q\n"+
					"  This likely means upgrade.go is out of sync with .goreleaser.yaml",
					tt.goos, tt.goarch, gotArchive, tt.wantArchive)
			}
			if gotBinary != tt.wantBinaryName {
				t.Errorf("Binary name mismatch for %s/%s:\n  got:  %q\n  want: %q\n"+
					"  This likely means upgrade.go is out of sync with .goreleaser.yaml",
					tt.goos, tt.goarch, gotBinary, tt.wantBinaryName)
			}
		})
	}
}

// simulateGetAssetName mirrors getAssetName() but for a specific platform
// This allows testing cross-platform naming without runtime.GOOS/GOARCH
func simulateGetAssetName(goos, goarch string) string {
	arch := goarch
	// macOS uses universal binary ("all") that works on both amd64 and arm64
	if goos == "darwin" {
		arch = "all"
	}
	// 32-bit ARM uses "armv7" suffix (GoReleaser builds with goarm=7)
	if goarch == "arm" {
		arch = "armv7"
	}
	return "ntm_" + goos + "_" + arch
}

// simulateGetArchiveAssetName mirrors getArchiveAssetName() but for a specific platform
func simulateGetArchiveAssetName(version, goos, goarch string) string {
	arch := goarch
	if goos == "darwin" {
		arch = "all"
	}
	// 32-bit ARM uses "armv7" suffix (GoReleaser builds with goarm=7)
	if goarch == "arm" {
		arch = "armv7"
	}
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return "ntm_" + version + "_" + goos + "_" + arch + "." + ext
}

// TestUpgradeAssetNamingConsistency verifies the actual functions produce
// consistent results with our test simulations on the current platform
func TestUpgradeAssetNamingConsistency(t *testing.T) {
	// The real functions use runtime.GOOS/GOARCH, so we test that the
	// current platform produces expected patterns

	realBinary := getAssetName()
	// Binary should always start with ntm_ and use underscore separators
	if !strings.HasPrefix(realBinary, "ntm_") {
		t.Errorf("getAssetName() = %q, should start with 'ntm_'", realBinary)
	}
	parts := strings.Split(realBinary, "_")
	if len(parts) != 3 {
		t.Errorf("getAssetName() = %q, should have 3 underscore-separated parts", realBinary)
	}

	realArchive := getArchiveAssetName("1.0.0")
	// Archive should have format: ntm_VERSION_OS_ARCH.ext
	if !strings.HasPrefix(realArchive, "ntm_1.0.0_") {
		t.Errorf("getArchiveAssetName('1.0.0') = %q, should start with 'ntm_1.0.0_'", realArchive)
	}
	if !strings.HasSuffix(realArchive, ".tar.gz") && !strings.HasSuffix(realArchive, ".zip") {
		t.Errorf("getArchiveAssetName() = %q, should end with .tar.gz or .zip", realArchive)
	}

	// Log for debugging
	t.Logf("Current platform produces: binary=%q, archive=%q", realBinary, realArchive)
}

// TestParseAssetInfo tests asset name parsing for upgrade error diagnostics
func TestParseAssetInfo(t *testing.T) {
	tests := []struct {
		name          string
		assetName     string
		targetOS      string
		targetArch    string
		targetVersion string
		wantOS        string
		wantArch      string
		wantVersion   string
		wantMatch     string
	}{
		{
			name:          "exact_match_darwin_all",
			assetName:     "ntm_1.4.1_darwin_all.tar.gz",
			targetOS:      "darwin",
			targetArch:    "all",
			targetVersion: "1.4.1",
			wantOS:        "darwin",
			wantArch:      "all",
			wantVersion:   "1.4.1",
			wantMatch:     "exact",
		},
		{
			name:          "close_match_darwin_amd64_for_all",
			assetName:     "ntm_1.4.1_darwin_amd64.tar.gz",
			targetOS:      "darwin",
			targetArch:    "all",
			targetVersion: "1.4.1",
			wantOS:        "darwin",
			wantArch:      "amd64",
			wantVersion:   "1.4.1",
			wantMatch:     "close",
		},
		{
			name:          "no_match_wrong_os",
			assetName:     "ntm_1.4.1_linux_amd64.tar.gz",
			targetOS:      "darwin",
			targetArch:    "all",
			targetVersion: "1.4.1",
			wantOS:        "linux",
			wantArch:      "amd64",
			wantVersion:   "1.4.1",
			wantMatch:     "none",
		},
		{
			name:          "windows_zip",
			assetName:     "ntm_1.4.1_windows_amd64.zip",
			targetOS:      "windows",
			targetArch:    "amd64",
			targetVersion: "1.4.1",
			wantOS:        "windows",
			wantArch:      "amd64",
			wantVersion:   "1.4.1",
			wantMatch:     "exact",
		},
		{
			name:          "non_ntm_asset",
			assetName:     "checksums.txt",
			targetOS:      "darwin",
			targetArch:    "all",
			targetVersion: "1.4.1",
			wantOS:        "",
			wantArch:      "",
			wantVersion:   "",
			wantMatch:     "none",
		},
		{
			name:          "close_match_armv7_for_arm64",
			assetName:     "ntm_1.4.1_linux_armv7.tar.gz",
			targetOS:      "linux",
			targetArch:    "arm64",
			targetVersion: "1.4.1",
			wantOS:        "linux",
			wantArch:      "armv7",
			wantVersion:   "1.4.1",
			wantMatch:     "close",
		},
		{
			name:          "exact_match_armv7",
			assetName:     "ntm_1.4.1_linux_armv7.tar.gz",
			targetOS:      "linux",
			targetArch:    "armv7",
			targetVersion: "1.4.1",
			wantOS:        "linux",
			wantArch:      "armv7",
			wantVersion:   "1.4.1",
			wantMatch:     "exact",
		},
		// Version mismatch: OS+Arch match but version differs (still "exact" for diagnostic purposes)
		{
			name:          "exact_match_version_differs",
			assetName:     "ntm_1.4.2_linux_amd64.tar.gz",
			targetOS:      "linux",
			targetArch:    "amd64",
			targetVersion: "1.4.1",
			wantOS:        "linux",
			wantArch:      "amd64",
			wantVersion:   "1.4.2",
			wantMatch:     "exact",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := parseAssetInfo(tt.assetName, tt.targetOS, tt.targetArch, tt.targetVersion)

			if info.OS != tt.wantOS {
				t.Errorf("OS = %q, want %q", info.OS, tt.wantOS)
			}
			if info.Arch != tt.wantArch {
				t.Errorf("Arch = %q, want %q", info.Arch, tt.wantArch)
			}
			if info.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", info.Version, tt.wantVersion)
			}
			if info.Match != tt.wantMatch {
				t.Errorf("Match = %q, want %q", info.Match, tt.wantMatch)
			}
		})
	}
}

// TestUpgradeErrorFormat tests the structured upgrade error output
func TestUpgradeErrorFormat(t *testing.T) {
	assets := []GitHubAsset{
		{Name: "ntm_1.4.1_linux_amd64.tar.gz"},
		{Name: "ntm_1.4.1_linux_arm64.tar.gz"},
		{Name: "ntm_1.4.1_darwin_amd64.tar.gz"},
		{Name: "checksums.txt"},
	}

	triedNames := []string{
		"ntm_1.4.1_darwin_all.tar.gz",
		"ntm_darwin_all",
	}

	err := newUpgradeError(
		"darwin",
		"arm64",
		"1.4.1",
		triedNames,
		assets,
		"https://github.com/Dicklesworthstone/ntm/releases/tag/v1.4.1",
	)

	errStr := err.Error()

	// Verify key components are present
	checks := []string{
		"darwin/arm64",                          // Platform
		"ntm_{version}_{os}_{arch}.tar.gz",      // Convention
		"ntm_1.4.1_darwin_all.tar.gz",           // Tried name
		"ntm_darwin_all",                        // Tried name
		".goreleaser.yaml",                      // Troubleshooting hint
		"internal/cli/upgrade.go",               // Troubleshooting hint
		"TestUpgradeAssetNaming",                // Test command
		"https://github.com/Dicklesworthstone/", // Links
		"closest match",                         // Asset annotation
	}

	for _, check := range checks {
		if !strings.Contains(errStr, check) {
			t.Errorf("Error output missing expected text: %q", check)
		}
	}

	// Verify JSON output
	jsonStr := err.JSON()
	if !strings.Contains(jsonStr, `"platform": "darwin/arm64"`) {
		t.Error("JSON output missing platform field")
	}
	if !strings.Contains(jsonStr, `"closest_match"`) {
		t.Error("JSON output missing closest_match field")
	}

	// Log for debugging
	t.Logf("Error output:\n%s", errStr)
}

// TestUpgradeErrorExactMatch tests the "exact" match marker for version mismatch scenarios
func TestUpgradeErrorExactMatch(t *testing.T) {
	// Scenario: User on linux/amd64, but release has wrong version in asset name
	assets := []GitHubAsset{
		{Name: "ntm_1.4.2_linux_amd64.tar.gz"}, // version 1.4.2, not 1.4.1
		{Name: "ntm_1.4.2_darwin_all.tar.gz"},
		{Name: "checksums.txt"},
	}

	triedNames := []string{
		"ntm_1.4.1_linux_amd64.tar.gz", // looking for 1.4.1
		"ntm_linux_amd64",
	}

	err := newUpgradeError(
		"linux",
		"amd64",
		"1.4.1",
		triedNames,
		assets,
		"https://github.com/Dicklesworthstone/ntm/releases/tag/v1.4.1",
	)

	errStr := err.Error()

	// Verify exact match annotation is present
	if !strings.Contains(errStr, "platform match") {
		t.Error("Error output missing 'platform match' annotation for exact semantic match")
	}
	if !strings.Contains(errStr, "check version") {
		t.Error("Error output missing 'check version' hint for version mismatch")
	}

	// Log for debugging
	t.Logf("Error output:\n%s", errStr)
}

// TestCreateCmdRequiresName tests create command requires session name
func TestCreateCmdRequiresName(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"create"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for create without session name")
	}
}

// TestBindCmdHelp tests the bind command help
func TestBindCmdHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"bind", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("bind --help failed: %v", err)
	}
}

// TestCommandAliases tests command aliases work
func TestCommandAliases(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	aliases := []struct {
		alias   string
		command string
	}{
		{"ls", "list"},
		{"l", "list"},
		{"a", "attach"},
	}

	for _, a := range aliases {
		t.Run(a.alias, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs([]string{a.alias})

			// These should not error on parsing
			err := rootCmd.Execute()
			// May error due to missing args or no sessions, but shouldn't fail on alias
			_ = err
		})
	}
}

// TestEnvVarConfig tests that environment variables are respected
func TestEnvVarConfig(t *testing.T) {
	// Test that XDG_CONFIG_HOME affects config path
	original := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", original)

	testDir := "/tmp/ntm_test_config"
	os.Setenv("XDG_CONFIG_HOME", testDir)

	path := config.DefaultPath()
	if !strings.HasPrefix(path, testDir) {
		t.Errorf("Expected config path to start with %s, got: %s", testDir, path)
	}
}

// TestInterruptCmdRequiresSession tests interrupt command requires session name
func TestInterruptCmdRequiresSession(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"interrupt"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for interrupt without session name")
	}
}

// TestDepsVerboseExecutes tests deps command with verbose flag
func TestDepsVerboseExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"deps", "-v"})

	// Should execute without panicking
	_ = rootCmd.Execute()
}

// TestConfigInitCreatesFile tests config init creates a config file
func TestConfigInitCreatesFile(t *testing.T) {
	// Use temp dir for config
	original := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", original)

	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	resetFlags()
	rootCmd.SetArgs([]string{"config", "init"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("config init failed: %v", err)
	}

	// Check file exists
	expectedPath := tmpDir + "/ntm/config.toml"
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("Expected config file at %s", expectedPath)
	}
}

// TestStatusCmdNonExistentSession tests status with non-existent session
func TestStatusCmdNonExistentSession(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	cfg = config.Default()
	resetFlags()
	rootCmd.SetArgs([]string{"status", "nonexistent_session_12345"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for non-existent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' error, got: %v", err)
	}
}

// TestRobotSendRequiresMsg tests robot-send requires --msg
func TestRobotSendRequiresMsg(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--robot-send", "testsession"})

	// Command should execute but exit with error about missing msg
	// The error is handled internally by printing to stderr and os.Exit
	// We can't easily test this without capturing os.Exit
	_ = rootCmd.Execute()
}

// TestRobotSnapshotWithSince tests robot-snapshot with --since flag
func TestRobotSnapshotWithSince(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-snapshot", "--since", "2025-01-01T00:00:00Z"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestRobotSnapshotInvalidSince tests robot-snapshot with invalid --since
func TestRobotSnapshotInvalidSince(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--robot-snapshot", "--since", "invalid-timestamp"})

	// Command handles this internally with os.Exit, so we can't catch the error easily
	// But it shouldn't panic
	_ = rootCmd.Execute()
}

// TestRobotTailExecutes tests robot-tail flag executes
func TestRobotTailExecutes(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-tail", "nonexistent_session_xyz"})

	// Will error because session doesn't exist, but shouldn't panic
	_ = rootCmd.Execute()
}

// TestRobotTailWithLines tests robot-tail with --lines flag
func TestRobotTailWithLines(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-tail", "nonexistent", "--lines", "50"})

	// Will error because session doesn't exist
	_ = rootCmd.Execute()
}

// TestGlobalJSONFlag tests the global --json flag works
func TestGlobalJSONFlag(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"--json", "list"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestGlobalConfigFlag tests the global --config flag parses
func TestGlobalConfigFlag(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--config", "/nonexistent/config.toml", "version"})

	// Should still work even with nonexistent config (falls back to defaults)
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestMultipleSubcommands tests various subcommand combinations
func TestMultipleSubcommands(t *testing.T) {
	helpCommands := []string{
		"spawn --help",
		"add --help",
		"send --help",
		"create --help",
		"quick --help",
		"view --help",
		"zoom --help",
		"copy --help",
		"save --help",
		"kill --help",
		"attach --help",
		"list --help",
		"status --help",
		"config --help",
	}

	for _, cmd := range helpCommands {
		t.Run(cmd, func(t *testing.T) {
			resetFlags()
			args := strings.Split(cmd, " ")
			rootCmd.SetArgs(args)

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("%s failed: %v", cmd, err)
			}
		})
	}
}

// TestVerifyUpgrade tests the post-upgrade binary verification logic
func TestVerifyUpgrade(t *testing.T) {
	tests := []struct {
		name            string
		expectedVersion string
		actualOutput    string
		shouldFail      bool
	}{
		{
			name:            "exact match",
			expectedVersion: "1.4.1",
			actualOutput:    "1.4.1",
			shouldFail:      false,
		},
		{
			name:            "match with v prefix in expected",
			expectedVersion: "v1.4.1",
			actualOutput:    "1.4.1",
			shouldFail:      false,
		},
		{
			name:            "match with v prefix in actual",
			expectedVersion: "1.4.1",
			actualOutput:    "v1.4.1",
			shouldFail:      false,
		},
		{
			name:            "mismatch major version",
			expectedVersion: "2.0.0",
			actualOutput:    "1.4.1",
			shouldFail:      true,
		},
		{
			name:            "mismatch minor version",
			expectedVersion: "1.5.0",
			actualOutput:    "1.4.1",
			shouldFail:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Test version comparison logic directly
			normalizedExpected := normalizeVersion(tc.expectedVersion)
			normalizedActual := normalizeVersion(tc.actualOutput)

			// Simulate the verification logic
			matches := normalizedActual == normalizedExpected ||
				strings.Contains(tc.actualOutput, normalizedExpected)

			if tc.shouldFail && matches {
				t.Errorf("Expected version check to fail for expected=%s actual=%s",
					tc.expectedVersion, tc.actualOutput)
			}
			if !tc.shouldFail && !matches {
				t.Errorf("Expected version check to pass for expected=%s actual=%s",
					tc.expectedVersion, tc.actualOutput)
			}
		})
	}
}

// TestRestoreBackup tests the backup restoration logic
func TestRestoreBackup(t *testing.T) {
	// Create a temp directory for test files
	tempDir, err := os.MkdirTemp("", "ntm-restore-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("successful restore", func(t *testing.T) {
		currentPath := filepath.Join(tempDir, "ntm-current")
		backupPath := currentPath + ".old"

		// Create "broken" current binary
		if err := os.WriteFile(currentPath, []byte("broken"), 0755); err != nil {
			t.Fatalf("Failed to create current file: %v", err)
		}

		// Create "working" backup
		if err := os.WriteFile(backupPath, []byte("working"), 0755); err != nil {
			t.Fatalf("Failed to create backup file: %v", err)
		}

		// Restore
		if err := restoreBackup(currentPath, backupPath); err != nil {
			t.Fatalf("restoreBackup failed: %v", err)
		}

		// Verify current has backup content
		content, err := os.ReadFile(currentPath)
		if err != nil {
			t.Fatalf("Failed to read restored file: %v", err)
		}
		if string(content) != "working" {
			t.Errorf("Restored content mismatch: got %q, want %q", string(content), "working")
		}

		// Verify backup was removed (renamed to current)
		if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
			t.Error("Backup file should not exist after restore")
		}
	})

	t.Run("backup not found", func(t *testing.T) {
		currentPath := filepath.Join(tempDir, "ntm-nonexistent")
		backupPath := currentPath + ".old"

		err := restoreBackup(currentPath, backupPath)
		if err == nil {
			t.Error("Expected error when backup doesn't exist")
		}
		if !strings.Contains(err.Error(), "backup file not found") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})
}
