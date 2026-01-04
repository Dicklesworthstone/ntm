package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeToolsPath returns the path to fake tools, or empty if not available
func fakeToolsPath(t *testing.T) string {
	t.Helper()

	// Get the project root by finding go.mod
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	// Walk up to find project root
	for dir := wd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			fakePath := filepath.Join(dir, "testdata", "faketools")
			if _, err := os.Stat(fakePath); err == nil {
				return fakePath
			}
			break
		}
	}

	return ""
}

// withFakeTools sets up PATH to include fake tools and returns a cleanup function
func withFakeTools(t *testing.T) func() {
	t.Helper()

	fakePath := fakeToolsPath(t)
	if fakePath == "" {
		t.Skip("testdata/faketools not found")
	}

	// Prepend fake tools to PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", fakePath+":"+oldPath)

	return func() {
		os.Setenv("PATH", oldPath)
	}
}

// TestBVAdapterVersionParsing tests version string parsing
func TestBVAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "bv 0.31.0",
			want:  Version{Major: 0, Minor: 31, Patch: 0, Raw: "bv 0.31.0"},
		},
		{
			input: "0.31.0",
			want:  Version{Major: 0, Minor: 31, Patch: 0, Raw: "0.31.0"},
		},
		{
			input: "bv version 1.2.3-beta",
			want:  Version{Major: 1, Minor: 2, Patch: 3, Raw: "bv version 1.2.3-beta"},
		},
		{
			input: "no version here",
			want:  Version{Raw: "no version here"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseVersion(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got.Major != tt.want.Major || got.Minor != tt.want.Minor || got.Patch != tt.want.Patch {
				t.Errorf("parseVersion() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestVersionCompare tests Version.Compare
func TestVersionCompare(t *testing.T) {
	tests := []struct {
		a, b Version
		want int
	}{
		{Version{1, 0, 0, ""}, Version{1, 0, 0, ""}, 0},
		{Version{1, 0, 0, ""}, Version{2, 0, 0, ""}, -1},
		{Version{2, 0, 0, ""}, Version{1, 0, 0, ""}, 1},
		{Version{1, 1, 0, ""}, Version{1, 0, 0, ""}, 1},
		{Version{1, 0, 1, ""}, Version{1, 0, 0, ""}, 1},
		{Version{0, 31, 0, ""}, Version{0, 30, 0, ""}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.a.String()+" vs "+tt.b.String(), func(t *testing.T) {
			if got := tt.a.Compare(tt.b); got != tt.want {
				t.Errorf("Compare() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestVersionAtLeast tests Version.AtLeast
func TestVersionAtLeast(t *testing.T) {
	tests := []struct {
		v, min Version
		want   bool
	}{
		{Version{1, 0, 0, ""}, Version{1, 0, 0, ""}, true},
		{Version{1, 1, 0, ""}, Version{1, 0, 0, ""}, true},
		{Version{0, 31, 0, ""}, Version{0, 30, 0, ""}, true},
		{Version{0, 29, 0, ""}, Version{0, 30, 0, ""}, false},
	}

	for _, tt := range tests {
		t.Run(tt.v.String()+" >= "+tt.min.String(), func(t *testing.T) {
			if got := tt.v.AtLeast(tt.min); got != tt.want {
				t.Errorf("AtLeast() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBVAdapterWithFakeTools tests the BV adapter with fake tools
func TestBVAdapterWithFakeTools(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	adapter := NewBVAdapter()
	ctx := context.Background()

	// Test Detect
	path, installed := adapter.Detect()
	if !installed {
		t.Fatal("Detect() should find fake bv")
	}
	if path == "" {
		t.Error("Detect() returned empty path")
	}

	// Test Version
	version, err := adapter.Version(ctx)
	if err != nil {
		t.Fatalf("Version() error: %v", err)
	}
	if version.Major != 0 || version.Minor != 31 {
		t.Errorf("Version() = %+v, want 0.31.x", version)
	}

	// Test Capabilities
	caps, err := adapter.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities() error: %v", err)
	}
	if len(caps) == 0 {
		t.Error("Capabilities() returned empty")
	}

	// Test Health
	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if !health.Healthy {
		t.Errorf("Health() = unhealthy: %s", health.Message)
	}

	// Test Info
	info, err := adapter.Info(ctx)
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	if !info.Installed {
		t.Error("Info() shows not installed")
	}
}

// TestBVAdapterRobotTriage tests robot-triage command
func TestBVAdapterRobotTriage(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	adapter := NewBVAdapter()
	ctx := context.Background()

	// Get triage from project root (where fixtures are)
	projectRoot := filepath.Dir(filepath.Dir(fakeToolsPath(t)))
	result, err := adapter.GetTriage(ctx, projectRoot)
	if err != nil {
		t.Fatalf("GetTriage() error: %v", err)
	}

	// Validate JSON structure
	var triage struct {
		GeneratedAt string `json:"generated_at"`
		DataHash    string `json:"data_hash"`
		Triage      struct {
			Meta struct {
				Version string `json:"version"`
			} `json:"meta"`
			QuickRef struct {
				OpenCount int `json:"open_count"`
			} `json:"quick_ref"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(result, &triage); err != nil {
		t.Fatalf("Failed to parse triage JSON: %v", err)
	}

	if triage.DataHash == "" {
		t.Error("Triage missing data_hash")
	}
	if triage.Triage.Meta.Version == "" {
		t.Error("Triage missing meta.version")
	}
}

// TestAdapterTimeout tests that adapters respect context timeout
func TestAdapterTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping timeout test in short mode")
	}

	cleanup := withFakeTools(t)
	defer cleanup()

	// Set timeout mode
	os.Setenv("FAKE_TOOL_MODE", "timeout")
	defer os.Unsetenv("FAKE_TOOL_MODE")

	adapter := NewBVAdapter()
	adapter.SetTimeout(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := adapter.Version(ctx)
	if err == nil {
		t.Error("Version() should timeout")
	}
}

// TestAdapterErrorMode tests error handling
func TestAdapterErrorMode(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	// Set error mode
	os.Setenv("FAKE_TOOL_MODE", "error")
	defer os.Unsetenv("FAKE_TOOL_MODE")

	adapter := NewBVAdapter()
	ctx := context.Background()

	_, err := adapter.Version(ctx)
	if err == nil {
		t.Error("Version() should return error in error mode")
	}
}

// TestBDAdapterWithFakeTools tests the BD adapter
func TestBDAdapterWithFakeTools(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	adapter := NewBDAdapter()
	ctx := context.Background()

	// Test Detect
	path, installed := adapter.Detect()
	if !installed {
		t.Fatal("Detect() should find fake bd")
	}
	if path == "" {
		t.Error("Detect() returned empty path")
	}

	// Test Version
	version, err := adapter.Version(ctx)
	if err != nil {
		t.Fatalf("Version() error: %v", err)
	}
	if version.Major != 1 || version.Minor != 0 {
		t.Errorf("Version() = %+v, want 1.0.x", version)
	}

	// Test Health
	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if !health.Healthy {
		t.Errorf("Health() = unhealthy: %s", health.Message)
	}
}

// TestCASSAdapterWithFakeTools tests the CASS adapter
func TestCASSAdapterWithFakeTools(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	adapter := NewCASSAdapter()
	ctx := context.Background()

	// Test Detect
	path, installed := adapter.Detect()
	if !installed {
		t.Fatal("Detect() should find fake cass")
	}
	if path == "" {
		t.Error("Detect() returned empty path")
	}

	// Test Version
	version, err := adapter.Version(ctx)
	if err != nil {
		t.Fatalf("Version() error: %v", err)
	}
	if version.Major != 0 || version.Minor != 5 {
		t.Errorf("Version() = %+v, want 0.5.x", version)
	}

	// Test Health
	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if !health.Healthy {
		t.Errorf("Health() = unhealthy: %s", health.Message)
	}
}

// TestAllAdaptersHaveConsistentInterface verifies all adapters implement Adapter correctly
func TestAllAdaptersHaveConsistentInterface(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	adapters := []struct {
		name    string
		adapter Adapter
	}{
		{"bv", NewBVAdapter()},
		{"bd", NewBDAdapter()},
		{"cass", NewCASSAdapter()},
		{"cm", NewCMAdapter()},
		{"s2p", NewS2PAdapter()},
		{"am", NewAMAdapter()},
	}

	ctx := context.Background()

	for _, tc := range adapters {
		t.Run(tc.name, func(t *testing.T) {
			// All adapters must have a name
			if tc.adapter.Name() == "" {
				t.Error("Name() returned empty")
			}

			// All adapters must implement Detect
			path, installed := tc.adapter.Detect()
			if !installed {
				t.Skipf("%s not installed (fake not found)", tc.name)
			}
			if path == "" {
				t.Error("Detect() returned empty path for installed tool")
			}

			// All adapters must implement Version
			version, err := tc.adapter.Version(ctx)
			if err != nil {
				t.Errorf("Version() error: %v", err)
			}
			if version.Raw == "" && version.Major == 0 && version.Minor == 0 {
				t.Error("Version() returned empty version")
			}

			// All adapters must implement Capabilities
			caps, err := tc.adapter.Capabilities(ctx)
			if err != nil {
				t.Errorf("Capabilities() error: %v", err)
			}
			_ = caps // May be empty, that's OK

			// All adapters must implement Health
			health, err := tc.adapter.Health(ctx)
			if err != nil {
				t.Errorf("Health() error: %v", err)
			}
			if health == nil {
				t.Error("Health() returned nil")
			}

			// All adapters must implement HasCapability
			_ = tc.adapter.HasCapability(ctx, CapRobotMode)

			// All adapters must implement Info
			info, err := tc.adapter.Info(ctx)
			if err != nil {
				t.Errorf("Info() error: %v", err)
			}
			if info == nil {
				t.Error("Info() returned nil")
			}
		})
	}
}

// TestToolNotInstalled tests behavior when tool is not installed
func TestToolNotInstalled(t *testing.T) {
	// Don't set up fake tools - test with non-existent binary
	adapter := NewBVAdapter()

	// Detect should return false
	_, installed := adapter.Detect()
	if installed {
		t.Skip("bv is actually installed, skipping not-installed test")
	}

	// Version should fail
	ctx := context.Background()
	_, err := adapter.Version(ctx)
	if err == nil {
		t.Error("Version() should fail for uninstalled tool")
	}

	// Health should indicate not installed
	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Error("Health() should be unhealthy for uninstalled tool")
	}
}

// TestRealToolsIfAvailable runs tests against real tools if installed
// This is skipped in CI without tools installed
func TestRealToolsIfAvailable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real tool tests in short mode")
	}

	ctx := context.Background()

	// Check for real bv
	if _, err := exec.LookPath("bv"); err == nil {
		t.Run("real_bv", func(t *testing.T) {
			adapter := NewBVAdapter()
			info, err := adapter.Info(ctx)
			if err != nil {
				t.Logf("Info() error (tool may be misconfigured): %v", err)
				return
			}
			t.Logf("Real bv version: %s", info.Version.String())
			t.Logf("Real bv capabilities: %v", info.Capabilities)
		})
	}

	// Check for real bd
	if _, err := exec.LookPath("bd"); err == nil {
		t.Run("real_bd", func(t *testing.T) {
			adapter := NewBDAdapter()
			info, err := adapter.Info(ctx)
			if err != nil {
				t.Logf("Info() error (tool may be misconfigured): %v", err)
				return
			}
			t.Logf("Real bd version: %s", info.Version.String())
		})
	}
}
