package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// InvalidateAvailabilityCache forces the next GetAvailability call to re-check.
func (a *RanoAdapter) InvalidateAvailabilityCache() {
	ranoAvailabilityMutex.Lock()
	ranoAvailabilityExpiry = time.Time{}
	ranoAvailabilityMutex.Unlock()
}

// GetProcessStats returns network stats for a specific PID.
func (a *RanoAdapter) GetProcessStats(ctx context.Context, pid int) (*RanoProcessStats, error) {
	return a.GetProcessStatsWithWindow(ctx, pid, "")
}

// GetProcessStatsWithWindow returns network stats for a specific PID with a time window override.
func (a *RanoAdapter) GetProcessStatsWithWindow(ctx context.Context, pid int, window string) (*RanoProcessStats, error) {
	if pid <= 0 {
		return nil, errors.New("rano process PID must be positive")
	}
	stats, err := a.GetAllProcessStatsWithWindow(ctx, window)
	if err != nil {
		return nil, err
	}
	for _, stat := range stats {
		if stat.PID == pid {
			return &stat, nil
		}
	}
	return &RanoProcessStats{PID: pid}, nil
}

func TestNewRanoAdapter(t *testing.T) {
	adapter := NewRanoAdapter()
	if adapter == nil {
		t.Fatal("NewRanoAdapter returned nil")
	}

	if adapter.Name() != ToolRano {
		t.Errorf("Expected name %s, got %s", ToolRano, adapter.Name())
	}

	if adapter.BinaryName() != "rano" {
		t.Errorf("Expected binary name 'rano', got %s", adapter.BinaryName())
	}
}

func TestRanoAdapterImplementsInterface(t *testing.T) {
	// Ensure RanoAdapter implements the Adapter interface
	var _ Adapter = (*RanoAdapter)(nil)
}

func TestRanoAdapterDetect(t *testing.T) {
	adapter := NewRanoAdapter()

	// Test detection - result depends on whether rano is installed
	path, installed := adapter.Detect()

	// If installed, path should be non-empty
	if installed && path == "" {
		t.Error("rano detected but path is empty")
	}

	// If not installed, path should be empty
	if !installed && path != "" {
		t.Errorf("rano not detected but path is %s", path)
	}
}

func TestRanoAvailabilityStruct(t *testing.T) {
	availability := RanoAvailability{
		Available:   true,
		Compatible:  true,
		Operational: true,
		CanReadProc: true,
		Version:     Version{Major: 1, Minor: 0, Patch: 0},
		Path:        "/usr/local/bin/rano",
		LastChecked: time.Now(),
	}

	if !availability.Available {
		t.Error("Expected Available to be true")
	}

	if !availability.Compatible {
		t.Error("Expected Compatible to be true")
	}

	if !availability.Operational {
		t.Error("Expected Operational to be true")
	}

	if !availability.CanReadProc {
		t.Error("Expected CanReadProc to be true")
	}
}

func TestRanoAdapterDatabaseResolution(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home)

	adapter := NewRanoAdapter()
	got, err := adapter.Database()
	if err != nil || got != filepath.Join(dir, RanoDefaultDatabase) {
		t.Fatalf("default database = %q, %v; want rano's observer.sqlite in the working directory", got, err)
	}
	for input, want := range map[string]string{
		"  /var/lib/rano/observer.sqlite ": "/var/lib/rano/observer.sqlite",
		"~/rano/observer.sqlite":           filepath.Join(home, "rano", "observer.sqlite"),
		"data/observer.sqlite":             filepath.Join(dir, "data", "observer.sqlite"),
		"":                                 filepath.Join(dir, RanoDefaultDatabase),
	} {
		adapter.SetDatabase(input)
		if got, err := adapter.Database(); err != nil || got != want {
			t.Errorf("SetDatabase(%q) -> %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestRanoAdapterCacheInvalidation(t *testing.T) {
	adapter := NewRanoAdapter()

	// Invalidate cache should not panic
	adapter.InvalidateAvailabilityCache()

	// Call again to ensure it's safe to call multiple times
	adapter.InvalidateAvailabilityCache()
}

func TestRanoAdapterHealthWhenNotInstalled(t *testing.T) {
	adapter := NewRanoAdapter()

	// If rano is not installed, Health should return non-healthy status
	_, installed := adapter.Detect()
	if installed {
		t.Skip("rano is installed, skipping not-installed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health returned unexpected error: %v", err)
	}

	if health.Healthy {
		t.Error("Expected unhealthy status when rano not installed")
	}

	if health.Message != "rano not installed" {
		t.Errorf("Expected message 'rano not installed', got %s", health.Message)
	}
}

// TestRanoHealthWhenInstalled is an end-to-end regression for issue #202: the
// probe ran the removed `rano status --json` (now "Unknown status flag: --json",
// exit 1), which made a healthy rano report a false "lacks CAP_NET_ADMIN"
// warning and silently disabled rano integration. A healthy, installed rano
// (whose `rano status` exits 0) must now report healthy. Runs only where rano
// is installed.
func TestRanoHealthWhenInstalled(t *testing.T) {
	adapter := NewRanoAdapter()
	if _, installed := adapter.Detect(); !installed {
		t.Skip("rano not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() returned error: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("installed rano reported unhealthy (stale `status --json` probe regression): %s", health.Message)
	}
}

func TestRanoAdapterIsAvailable(t *testing.T) {
	adapter := NewRanoAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// IsAvailable should not panic regardless of whether rano is installed
	available := adapter.IsAvailable(ctx)

	// If rano is not installed, should return false
	_, installed := adapter.Detect()
	if !installed && available {
		t.Error("IsAvailable returned true but rano is not installed")
	}
}

func TestRanoAdapterGetAvailability(t *testing.T) {
	adapter := NewRanoAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// GetAvailability should not panic and should return valid struct
	availability, err := adapter.GetAvailability(ctx)
	if err != nil {
		t.Fatalf("GetAvailability returned unexpected error: %v", err)
	}

	if availability == nil {
		t.Fatal("GetAvailability returned nil")
	}

	// LastChecked should be set
	if availability.LastChecked.IsZero() {
		t.Error("LastChecked should be set")
	}

	// If not installed, Available should be false
	_, installed := adapter.Detect()
	if !installed && availability.Available {
		t.Error("Available should be false when rano not installed")
	}
}

func TestRanoAdapterGetAvailabilityCaching(t *testing.T) {
	adapter := NewRanoAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First call
	availability1, err := adapter.GetAvailability(ctx)
	if err != nil {
		t.Fatalf("First GetAvailability returned error: %v", err)
	}

	// Second call should return cached result
	availability2, err := adapter.GetAvailability(ctx)
	if err != nil {
		t.Fatalf("Second GetAvailability returned error: %v", err)
	}

	// LastChecked should be the same (cached)
	if !availability1.LastChecked.Equal(availability2.LastChecked) {
		t.Error("Expected cached result with same LastChecked time")
	}
}

func TestToolRanoInAllTools(t *testing.T) {
	tools := AllTools()
	found := false
	for _, tool := range tools {
		if tool == ToolRano {
			found = true
			break
		}
	}

	if !found {
		t.Error("ToolRano not found in AllTools()")
	}
}

func TestRanoMinVersionCompatibility(t *testing.T) {
	tests := []struct {
		name     string
		version  Version
		expected bool
	}{
		{
			name:     "exact min version",
			version:  Version{Major: 0, Minor: 1, Patch: 0},
			expected: true,
		},
		{
			name:     "above min version",
			version:  Version{Major: 1, Minor: 0, Patch: 0},
			expected: true,
		},
		{
			name:     "minor above",
			version:  Version{Major: 0, Minor: 2, Patch: 0},
			expected: true,
		},
		{
			name:     "below min version",
			version:  Version{Major: 0, Minor: 0, Patch: 9},
			expected: false,
		},
		{
			name:     "zero version",
			version:  Version{Major: 0, Minor: 0, Patch: 0},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ranoCompatible(tt.version)
			if result != tt.expected {
				t.Errorf("ranoCompatible(%v) = %v, want %v", tt.version, result, tt.expected)
			}
		})
	}
}

func TestRanoAvailabilityTTL(t *testing.T) {
	// Verify that the TTL is set to 2 minutes (shorter due to permission changes)
	if ranoAvailabilityTTL != 2*time.Minute {
		t.Errorf("Expected rano availability TTL of 2m, got %v", ranoAvailabilityTTL)
	}
}

func TestRanoAdapterCheckProcAccess(t *testing.T) {
	adapter := NewRanoAdapter()

	// checkProcAccess should work on Linux systems
	canAccess := adapter.checkProcAccess()

	// On Linux with /proc available, this should return true
	// Just verify it doesn't panic
	t.Logf("checkProcAccess returned: %v", canAccess)
}

// TestRanoStatsReadInstalledRanoObservation has the installed rano record this
// test process's own loopback connection into a fresh observer database, then
// reads it back through the adapter's export path. rano's own export of the
// same database is the oracle for the count.
func TestRanoStatsReadInstalledRanoObservation(t *testing.T) {
	if _, err := exec.LookPath("rano"); err != nil {
		t.Skip("rano not installed")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	server := <-accepted
	t.Cleanup(func() { _ = server.Close() })

	pid := strconv.Itoa(os.Getpid())
	db := filepath.Join(t.TempDir(), "observer.sqlite")
	if out, err := exec.Command("rano", "--once", "--pid", pid, "--sqlite", db, "--no-dns", "--no-banner", "--summary-only").CombinedOutput(); err != nil {
		t.Fatalf("rano --once: %v\n%s", err, out)
	}
	export, err := exec.Command("rano", "export", "--format", "jsonl", "--sqlite", db, "--fields", "event,pid").Output()
	if err != nil {
		t.Fatalf("rano export: %v", err)
	}
	want := 0
	for _, line := range strings.Split(string(export), "\n") {
		var row struct {
			Event string `json:"event"`
			PID   int    `json:"pid"`
		}
		if json.Unmarshal([]byte(line), &row) == nil && row.Event == "connect" && row.PID == os.Getpid() {
			want++
		}
	}
	if want == 0 {
		t.Fatalf("rano recorded no connect for pid %s; export:\n%s", pid, export)
	}

	adapter := NewRanoAdapter()
	adapter.SetDatabase(db)
	stats, err := adapter.GetProcessStatsWithWindow(context.Background(), os.Getpid(), "5m")
	if err != nil {
		t.Fatalf("GetProcessStatsWithWindow: %v", err)
	}
	if stats.ConnectionCount != want || stats.LastConnection == "" {
		t.Fatalf("stats = %+v, rano exported %d connects for this pid", stats, want)
	}
}
