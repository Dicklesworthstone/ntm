package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestJFPAdapterVersionParsing tests JFP version string parsing
func TestJFPAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "jfp/1.0.0 linux-x64 node-v24.3.0",
			want:  Version{Major: 1, Minor: 0, Patch: 0, Raw: "jfp/1.0.0 linux-x64 node-v24.3.0"},
		},
		{
			input: "jfp/2.1.3 darwin-arm64 node-v22.0.0",
			want:  Version{Major: 2, Minor: 1, Patch: 3, Raw: "jfp/2.1.3 darwin-arm64 node-v22.0.0"},
		},
		{
			input: "jfp/0.9.12",
			want:  Version{Major: 0, Minor: 9, Patch: 12, Raw: "jfp/0.9.12"},
		},
		{
			input: "no version",
			want:  Version{Raw: "no version"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseJFPVersion(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseJFPVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got.Major != tt.want.Major || got.Minor != tt.want.Minor || got.Patch != tt.want.Patch {
				t.Errorf("parseJFPVersion() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestSLBAdapterVersionParsing tests SLB version string parsing
func TestSLBAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "slb 0.1.0\n  commit:  cc17518fe7d699363f4bcb48670ed4a3bbc71127\n  built:   2025-12-25T03:35:46Z",
			want:  Version{Major: 0, Minor: 1, Patch: 0, Raw: "slb 0.1.0\n  commit:  cc17518fe7d699363f4bcb48670ed4a3bbc71127\n  built:   2025-12-25T03:35:46Z"},
		},
		{
			input: "slb 1.2.3",
			want:  Version{Major: 1, Minor: 2, Patch: 3, Raw: "slb 1.2.3"},
		},
		{
			input: "slb 0.0.1\n  commit:  abc123",
			want:  Version{Major: 0, Minor: 0, Patch: 1, Raw: "slb 0.0.1\n  commit:  abc123"},
		},
		{
			input: "no version",
			want:  Version{Raw: "no version"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseSLBVersion(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSLBVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got.Major != tt.want.Major || got.Minor != tt.want.Minor || got.Patch != tt.want.Patch {
				t.Errorf("parseSLBVersion() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestACFSAdapterVersionParsing tests ACFS version string parsing
func TestACFSAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "0.1.0",
			want:  Version{Major: 0, Minor: 1, Patch: 0, Raw: "0.1.0"},
		},
		{
			input: "1.2.3",
			want:  Version{Major: 1, Minor: 2, Patch: 3, Raw: "1.2.3"},
		},
		{
			input: "2.0.0-beta",
			want:  Version{Major: 2, Minor: 0, Patch: 0, Raw: "2.0.0-beta"},
		},
		{
			input: "no version",
			want:  Version{Raw: "no version"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseACFSVersion(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseACFSVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got.Major != tt.want.Major || got.Minor != tt.want.Minor || got.Patch != tt.want.Patch {
				t.Errorf("parseACFSVersion() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestMSAdapterVersionParsing tests MS version string parsing
func TestMSAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "ms 0.1.0",
			want:  Version{Major: 0, Minor: 1, Patch: 0, Raw: "0.1.0"},
		},
		{
			input: "0.2.5",
			want:  Version{Major: 0, Minor: 2, Patch: 5, Raw: "0.2.5"},
		},
		{
			input: "ms 1.0.0-beta",
			want:  Version{Major: 1, Minor: 0, Patch: 0, Raw: "1.0.0-beta"},
		},
		{
			input: "no version",
			want:  Version{Raw: "no version"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseMSVersion(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseMSVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got.Major != tt.want.Major || got.Minor != tt.want.Minor || got.Patch != tt.want.Patch {
				t.Errorf("parseMSVersion() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestGIILAdapterVersionParsing tests GIIL version string parsing
// GIIL uses the generic parseVersion function which extracts X.Y.Z via regex
func TestGIILAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "giil version 3.1.0 (Hybrid Edition)",
			want:  Version{Major: 3, Minor: 1, Patch: 0, Raw: "giil version 3.1.0 (Hybrid Edition)"},
		},
		{
			input: "giil version 1.0.0",
			want:  Version{Major: 1, Minor: 0, Patch: 0, Raw: "giil version 1.0.0"},
		},
		{
			input: "3.2.1",
			want:  Version{Major: 3, Minor: 2, Patch: 1, Raw: "3.2.1"},
		},
		{
			input: "no version",
			want:  Version{Raw: "no version"},
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

// TestRUAdapterVersionParsing tests RU version string parsing
// RU uses the generic parseVersion function which extracts X.Y.Z via regex
func TestRUAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "ru 0.3.2",
			want:  Version{Major: 0, Minor: 3, Patch: 2, Raw: "ru 0.3.2"},
		},
		{
			input: "ru version 1.0.0",
			want:  Version{Major: 1, Minor: 0, Patch: 0, Raw: "ru version 1.0.0"},
		},
		{
			input: "0.5.1",
			want:  Version{Major: 0, Minor: 5, Patch: 1, Raw: "0.5.1"},
		},
		{
			input: "no version",
			want:  Version{Raw: "no version"},
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

// TestXFAdapterVersionParsing tests XF version string parsing
// XF uses the generic parseVersion function which extracts X.Y.Z via regex
func TestXFAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "xf 0.2.1",
			want:  Version{Major: 0, Minor: 2, Patch: 1, Raw: "xf 0.2.1"},
		},
		{
			input: "xf version 1.0.0-beta",
			want:  Version{Major: 1, Minor: 0, Patch: 0, Raw: "xf version 1.0.0-beta"},
		},
		{
			input: "0.1.5",
			want:  Version{Major: 0, Minor: 1, Patch: 5, Raw: "0.1.5"},
		},
		{
			input: "no version",
			want:  Version{Raw: "no version"},
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

// TestDCGAdapterVersionParsing tests DCG version string parsing
// DCG uses the generic parseVersion function which extracts X.Y.Z via regex
func TestDCGAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "dcg 0.1.0",
			want:  Version{Major: 0, Minor: 1, Patch: 0, Raw: "dcg 0.1.0"},
		},
		{
			input: "dcg version 1.2.3",
			want:  Version{Major: 1, Minor: 2, Patch: 3, Raw: "dcg version 1.2.3"},
		},
		{
			input: "0.0.5",
			want:  Version{Major: 0, Minor: 0, Patch: 5, Raw: "0.0.5"},
		},
		{
			input: "no version",
			want:  Version{Raw: "no version"},
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

func TestDCGAvailabilityWithFakeTool(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	adapter := NewDCGAdapter()
	adapter.InvalidateAvailabilityCache()

	ctx := context.Background()
	availability, err := adapter.GetAvailability(ctx)
	if err != nil {
		t.Fatalf("GetAvailability() error = %v", err)
	}
	if !availability.Available {
		t.Fatalf("expected dcg to be available on PATH")
	}
	if !availability.Compatible {
		t.Fatalf("expected dcg to be compatible, got version %s", availability.Version.String())
	}
	if availability.Path == "" {
		t.Fatalf("expected dcg path to be set")
	}
	if !adapter.IsAvailable(ctx) {
		t.Fatalf("expected IsAvailable() to return true")
	}
}

func TestDCGAvailabilityMissingBinary(t *testing.T) {
	adapter := NewDCGAdapter()

	emptyPath := t.TempDir()
	t.Setenv("PATH", emptyPath)
	adapter.InvalidateAvailabilityCache()

	ctx := context.Background()
	availability, err := adapter.GetAvailability(ctx)
	if err != nil {
		t.Fatalf("GetAvailability() error = %v", err)
	}
	if availability.Available {
		t.Fatalf("expected dcg to be unavailable")
	}
	if availability.Compatible {
		t.Fatalf("expected dcg to be incompatible when missing")
	}
	if availability.Path != "" {
		t.Fatalf("expected dcg path to be empty when missing")
	}
	if adapter.IsAvailable(ctx) {
		t.Fatalf("expected IsAvailable() to return false when missing")
	}
}

func TestDCGAvailabilityIncompatibleVersion(t *testing.T) {
	dir := t.TempDir()
	fakeDCG := filepath.Join(dir, "dcg")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo \"dcg 0.0.1\"; exit 0; fi\nexit 0\n"
	if err := os.WriteFile(fakeDCG, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake dcg: %v", err)
	}

	t.Setenv("PATH", dir)

	adapter := NewDCGAdapter()
	adapter.InvalidateAvailabilityCache()

	ctx := context.Background()
	availability, err := adapter.GetAvailability(ctx)
	if err != nil {
		t.Fatalf("GetAvailability() error = %v", err)
	}
	if !availability.Available {
		t.Fatalf("expected dcg to be found on PATH")
	}
	if availability.Compatible {
		t.Fatalf("expected dcg to be incompatible with version %s", availability.Version.String())
	}
	if adapter.IsAvailable(ctx) {
		t.Fatalf("expected IsAvailable() to return false for incompatible version")
	}
}

// TestUBSAdapterVersionParsing tests UBS version string parsing
// UBS uses the generic parseVersion function which extracts X.Y.Z via regex
func TestUBSAdapterVersionParsing(t *testing.T) {
	tests := []struct {
		input   string
		want    Version
		wantErr bool
	}{
		{
			input: "ubs 0.4.2",
			want:  Version{Major: 0, Minor: 4, Patch: 2, Raw: "ubs 0.4.2"},
		},
		{
			input: "ubs version 1.0.0",
			want:  Version{Major: 1, Minor: 0, Patch: 0, Raw: "ubs version 1.0.0"},
		},
		{
			input: "0.3.1",
			want:  Version{Major: 0, Minor: 3, Patch: 1, Raw: "0.3.1"},
		},
		{
			input: "no version",
			want:  Version{Raw: "no version"},
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

// TestJFPAdapterWithFakeTools tests the JFP adapter with fake tools
func TestJFPAdapterWithFakeTools(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	adapter := NewJFPAdapter()
	ctx := context.Background()

	// Test Detect
	path, installed := adapter.Detect()
	if !installed {
		t.Fatal("Detect() should find fake jfp")
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

// TestJFPAdapterMethods tests JFP-specific adapter methods
func TestJFPAdapterMethods(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	adapter := NewJFPAdapter()
	ctx := context.Background()

	// Test List
	result, err := adapter.List(ctx)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if !json.Valid(result) {
		t.Error("List() returned invalid JSON")
	}

	// Test Status
	result, err = adapter.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if !json.Valid(result) {
		t.Error("Status() returned invalid JSON")
	}

	// Test Search
	result, err = adapter.Search(ctx, "test")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if !json.Valid(result) {
		t.Error("Search() returned invalid JSON")
	}

	// Test Show
	result, err = adapter.Show(ctx, "test-prompt")
	if err != nil {
		t.Fatalf("Show() error: %v", err)
	}
	if !json.Valid(result) {
		t.Error("Show() returned invalid JSON")
	}

	// Test Suggest
	result, err = adapter.Suggest(ctx, "test task")
	if err != nil {
		t.Fatalf("Suggest() error: %v", err)
	}
	if !json.Valid(result) {
		t.Error("Suggest() returned invalid JSON")
	}

	// Test Install
	result, err = adapter.Install(ctx, []string{"test-prompt"}, "")
	if err != nil {
		t.Fatalf("Install() error: %v", err)
	}
	if !json.Valid(result) {
		t.Error("Install() returned invalid JSON")
	}

	// Test Export
	result, err = adapter.Export(ctx, []string{"test-prompt"}, "skill")
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	if !json.Valid(result) {
		t.Error("Export() returned invalid JSON")
	}

	// Test Update
	result, err = adapter.Update(ctx)
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if !json.Valid(result) {
		t.Error("Update() returned invalid JSON")
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

func TestBVAdapterRobotModes(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	adapter := NewBVAdapter()
	ctx := context.Background()
	projectRoot := filepath.Dir(filepath.Dir(fakeToolsPath(t)))

	type modeTest struct {
		name string
		key  string
		call func() (json.RawMessage, error)
	}

	tests := []modeTest{
		{
			name: "triage_by_label",
			key:  "triage_by_label",
			call: func() (json.RawMessage, error) {
				return adapter.GetGroupedTriage(ctx, projectRoot, BVGroupedTriageOptions{ByLabel: true})
			},
		},
		{
			name: "triage_by_track",
			key:  "triage_by_track",
			call: func() (json.RawMessage, error) {
				return adapter.GetGroupedTriage(ctx, projectRoot, BVGroupedTriageOptions{ByTrack: true})
			},
		},
		{
			name: "alerts",
			key:  "alerts",
			call: func() (json.RawMessage, error) {
				return adapter.GetAlerts(ctx, projectRoot, BVAlertOptions{Severity: "warning"})
			},
		},
		{
			name: "graph",
			key:  "graph",
			call: func() (json.RawMessage, error) {
				return adapter.GetGraph(ctx, projectRoot, BVGraphOptions{Format: "json"})
			},
		},
		{
			name: "history",
			key:  "stats",
			call: func() (json.RawMessage, error) {
				return adapter.GetHistory(ctx, projectRoot)
			},
		},
		{
			name: "burndown",
			key:  "progress",
			call: func() (json.RawMessage, error) {
				return adapter.GetBurndown(ctx, projectRoot, "s1")
			},
		},
		{
			name: "forecast",
			key:  "forecasts",
			call: func() (json.RawMessage, error) {
				return adapter.GetForecast(ctx, projectRoot, "all")
			},
		},
		{
			name: "suggest",
			key:  "suggestions",
			call: func() (json.RawMessage, error) {
				return adapter.GetSuggestions(ctx, projectRoot)
			},
		},
		{
			name: "impact",
			key:  "impact_score",
			call: func() (json.RawMessage, error) {
				return adapter.GetImpact(ctx, projectRoot, "internal/foo.go")
			},
		},
		{
			name: "search",
			key:  "results",
			call: func() (json.RawMessage, error) {
				return adapter.GetSearch(ctx, projectRoot, "test query")
			},
		},
		{
			name: "label_attention",
			key:  "labels",
			call: func() (json.RawMessage, error) {
				return adapter.GetLabelAttention(ctx, projectRoot, 5)
			},
		},
		{
			name: "label_flow",
			key:  "flow_matrix",
			call: func() (json.RawMessage, error) {
				return adapter.GetLabelFlow(ctx, projectRoot)
			},
		},
		{
			name: "label_health",
			key:  "results",
			call: func() (json.RawMessage, error) {
				return adapter.GetLabelHealth(ctx, projectRoot)
			},
		},
		{
			name: "file_beads",
			key:  "files",
			call: func() (json.RawMessage, error) {
				return adapter.GetFileBeads(ctx, projectRoot, "internal/foo.go", 5)
			},
		},
		{
			name: "file_hotspots",
			key:  "hotspots",
			call: func() (json.RawMessage, error) {
				return adapter.GetFileHotspots(ctx, projectRoot, 5)
			},
		},
		{
			name: "file_relations",
			key:  "relations",
			call: func() (json.RawMessage, error) {
				return adapter.GetFileRelations(ctx, projectRoot, "internal/foo.go", 5, 0.4)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := tt.call()
			if err != nil {
				t.Fatalf("%s error: %v", tt.name, err)
			}
			if !json.Valid(raw) {
				t.Fatalf("%s returned invalid JSON", tt.name)
			}

			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("%s unmarshal error: %v", tt.name, err)
			}
			if _, ok := payload[tt.key]; !ok {
				t.Errorf("%s missing key %q", tt.name, tt.key)
			}
		})
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

	// Run with a test-level deadline to prevent test from hanging forever
	done := make(chan struct{})
	var versionErr error
	go func() {
		_, versionErr = adapter.Version(ctx)
		close(done)
	}()

	select {
	case <-done:
		if versionErr == nil {
			t.Error("Version() should timeout")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Version() did not return within 5s - context timeout not working")
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

	fakePath := fakeToolsPath(t)
	if fakePath == "" {
		t.Skip("testdata/faketools not found")
	}
	tempPath := t.TempDir()
	if err := os.Symlink(filepath.Join(fakePath, "bd"), filepath.Join(tempPath, "br")); err != nil {
		t.Fatalf("symlink fake br: %v", err)
	}
	t.Setenv("PATH", tempPath+":"+os.Getenv("PATH"))

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

func TestBDAdapterPrefersBrWhenAvailable(t *testing.T) {
	tmpDir := t.TempDir()
	brPath := filepath.Join(tmpDir, "br")
	script := "#!/bin/sh\necho 'br 9.9.9'\n"
	if err := os.WriteFile(brPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake br: %v", err)
	}

	t.Setenv("PATH", tmpDir+":"+os.Getenv("PATH"))

	adapter := NewBDAdapter()

	path, installed := adapter.Detect()
	if !installed {
		t.Fatal("Detect() should find fake br")
	}
	if !strings.HasSuffix(path, string(filepath.Separator)+"br") && filepath.Base(path) != "br" {
		t.Fatalf("Detect() should prefer br, got %q", path)
	}
}

func TestBDAdapterFallsBackToBdWhenBrVersionFails(t *testing.T) {
	tmpDir := t.TempDir()
	brPath := filepath.Join(tmpDir, "br")
	brScript := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\n  echo 'broken br' >&2\n  exit 1\nfi\necho '{\"from\":\"br\"}'\n"
	if err := os.WriteFile(brPath, []byte(brScript), 0o755); err != nil {
		t.Fatalf("write fake br: %v", err)
	}

	bdPath := filepath.Join(tmpDir, "bd")
	bdScript := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\n  echo 'bd 1.2.3'\n  exit 0\nfi\necho '{\"from\":\"bd\"}'\n"
	if err := os.WriteFile(bdPath, []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", tmpDir+":"+os.Getenv("PATH"))

	adapter := NewBDAdapter()
	ctx := context.Background()

	path, installed := adapter.Detect()
	if !installed {
		t.Fatal("Detect() should find a working beads binary")
	}
	if filepath.Base(path) != "bd" {
		t.Fatalf("Detect() should fall back to bd, got %q", path)
	}

	version, err := adapter.Version(ctx)
	if err != nil {
		t.Fatalf("Version() error: %v", err)
	}
	if version.Major != 1 || version.Minor != 2 || version.Patch != 3 {
		t.Fatalf("Version() = %+v, want 1.2.3", version)
	}

	output, err := adapter.GetStats(ctx, "")
	if err != nil {
		t.Fatalf("GetStats() error: %v", err)
	}
	if string(output) != "{\"from\":\"bd\"}\n" {
		t.Fatalf("GetStats() should use bd fallback, got %q", string(output))
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
		{"jfp", NewJFPAdapter()},
		{"dcg", NewDCGAdapter()},
		{"slb", NewSLBAdapter()},
		{"acfs", NewACFSAdapter()},
		{"ms", NewMSAdapter()},
		{"giil", NewGIILAdapter()},
		{"ru", NewRUAdapter()},
		{"xf", NewXFAdapter()},
		{"ubs", NewUBSAdapter()},
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

	// Check for real jfp
	if _, err := exec.LookPath("jfp"); err == nil {
		t.Run("real_jfp", func(t *testing.T) {
			adapter := NewJFPAdapter()
			info, err := adapter.Info(ctx)
			if err != nil {
				t.Logf("Info() error (tool may be misconfigured): %v", err)
				return
			}
			t.Logf("Real jfp version: %s", info.Version.String())
			t.Logf("Real jfp capabilities: %v", info.Capabilities)
			t.Logf("Real jfp health: %v", info.Health.Message)
		})
	}
}
