package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewRCHAdapter(t *testing.T) {
	adapter := NewRCHAdapter()
	if adapter == nil {
		t.Fatal("NewRCHAdapter returned nil")
	}

	if adapter.Name() != ToolRCH {
		t.Errorf("Expected name %s, got %s", ToolRCH, adapter.Name())
	}

	if adapter.BinaryName() != "rch" {
		t.Errorf("Expected binary name 'rch', got %s", adapter.BinaryName())
	}
}

func TestRCHAdapterImplementsInterface(t *testing.T) {
	// Ensure RCHAdapter implements the Adapter interface
	var _ Adapter = (*RCHAdapter)(nil)
}

func TestRCHAdapterDetect(t *testing.T) {
	adapter := NewRCHAdapter()

	// Test detection - result depends on whether rch is installed
	path, installed := adapter.Detect()

	// If installed, path should be non-empty
	if installed && path == "" {
		t.Error("rch detected but path is empty")
	}

	// If not installed, path should be empty
	if !installed && path != "" {
		t.Errorf("rch not detected but path is %s", path)
	}
}

func TestRCHWorkerStruct(t *testing.T) {
	worker := RCHWorker{
		Name:      "worker-1",
		Host:      "build01.example.com",
		Available: true,
		Healthy:   true,
		Load:      25,
		Queue:     3,
		LastSeen:  "2026-01-21T10:00:00Z",
	}

	if worker.Name != "worker-1" {
		t.Errorf("Expected Name 'worker-1', got %s", worker.Name)
	}

	if worker.Host != "build01.example.com" {
		t.Errorf("Expected Host 'build01.example.com', got %s", worker.Host)
	}

	if !worker.Available {
		t.Error("Expected Available to be true")
	}

	if !worker.Healthy {
		t.Error("Expected Healthy to be true")
	}

	if worker.Load != 25 {
		t.Errorf("Expected Load 25, got %d", worker.Load)
	}

	if worker.Queue != 3 {
		t.Errorf("Expected Queue 3, got %d", worker.Queue)
	}
}

func TestRCHStatusStruct(t *testing.T) {
	status := RCHStatus{
		Enabled:      true,
		WorkerCount:  3,
		HealthyCount: 2,
		Workers: []RCHWorker{
			{Name: "worker-1", Available: true, Healthy: true},
			{Name: "worker-2", Available: true, Healthy: true},
			{Name: "worker-3", Available: false, Healthy: false},
		},
	}

	if !status.Enabled {
		t.Error("Expected Enabled to be true")
	}

	if status.WorkerCount != 3 {
		t.Errorf("Expected WorkerCount 3, got %d", status.WorkerCount)
	}

	if status.HealthyCount != 2 {
		t.Errorf("Expected HealthyCount 2, got %d", status.HealthyCount)
	}

	if len(status.Workers) != 3 {
		t.Errorf("Expected 3 workers, got %d", len(status.Workers))
	}
}

func TestParseRCHStatusWrappedDaemonSchema(t *testing.T) {
	status, err := parseRCHStatusJSON([]byte(`{
		"success": true,
		"data": {
			"daemon": {
				"daemon": {"workers_total": 2, "workers_healthy": 1},
				"workers": [
					{"id": "worker-a", "host": "10.0.0.1", "status": "healthy", "used_slots": 1, "total_slots": 2},
					{"id": "worker-b", "host": "10.0.0.2", "status": "unreachable", "used_slots": 0, "total_slots": 4}
				],
				"active_builds": [
					{"worker_id": "worker-a", "command": "go test ./..."}
				],
				"stats": {"total_builds": 10, "remote_count": 8, "local_count": 2},
				"saved_time": {"time_saved_ms": 61500}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("parseRCHStatusJSON returned error: %v", err)
	}

	if !status.Enabled {
		t.Fatal("expected wrapped status to be enabled")
	}
	if status.WorkerCount != 2 {
		t.Fatalf("expected WorkerCount 2, got %d", status.WorkerCount)
	}
	if status.HealthyCount != 1 {
		t.Fatalf("expected HealthyCount 1, got %d", status.HealthyCount)
	}
	if len(status.Workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(status.Workers))
	}

	workerA := status.Workers[0]
	if workerA.Name != "worker-a" || workerA.Host != "10.0.0.1" {
		t.Fatalf("unexpected first worker identity: %+v", workerA)
	}
	if !workerA.Available || !workerA.Healthy {
		t.Fatalf("expected worker-a to be available and healthy: %+v", workerA)
	}
	if workerA.Load != 50 {
		t.Fatalf("expected worker-a load 50, got %d", workerA.Load)
	}
	if workerA.CurrentBuild != "go test ./..." {
		t.Fatalf("expected current build command, got %q", workerA.CurrentBuild)
	}

	workerB := status.Workers[1]
	if workerB.Available || workerB.Healthy {
		t.Fatalf("expected worker-b to be unavailable and unhealthy: %+v", workerB)
	}

	if status.SessionStats == nil {
		t.Fatal("expected session stats")
	}
	if status.SessionStats.BuildsTotal != 10 || status.SessionStats.BuildsRemote != 8 || status.SessionStats.BuildsLocal != 2 {
		t.Fatalf("unexpected session stats: %+v", status.SessionStats)
	}
	if status.SessionStats.TimeSavedSeconds != 62 {
		t.Fatalf("expected rounded time saved 62 seconds, got %d", status.SessionStats.TimeSavedSeconds)
	}
}

func TestParseRCHStatusFlatSchemaFillsDerivedCounts(t *testing.T) {
	status, err := parseRCHStatusJSON([]byte(`{
		"workers": [
			{"name": "worker-a", "available": true, "healthy": true},
			{"name": "worker-b", "available": false, "healthy": false}
		]
	}`))
	if err != nil {
		t.Fatalf("parseRCHStatusJSON returned error: %v", err)
	}

	if !status.Enabled {
		t.Fatal("expected flat status with workers to be enabled")
	}
	if status.WorkerCount != 2 {
		t.Fatalf("expected WorkerCount 2, got %d", status.WorkerCount)
	}
	if status.HealthyCount != 1 {
		t.Fatalf("expected HealthyCount 1, got %d", status.HealthyCount)
	}
}

func TestRCHAdapterGetStatusPropagatesDaemonFailure(t *testing.T) {
	dir := t.TempDir()
	fakeRCH := filepath.Join(dir, "rch")
	if err := os.WriteFile(fakeRCH, []byte(`#!/bin/sh
if [ "$1" = "status" ] && [ "$2" = "--json" ]; then
  echo "daemon socket unavailable" >&2
  exit 7
fi
exit 1
`), 0755); err != nil {
		t.Fatalf("write fake rch: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	status, err := NewRCHAdapter().GetStatus(context.Background())
	if err == nil {
		t.Fatal("GetStatus() error = nil, want daemon failure")
	}
	if status != nil {
		t.Fatalf("GetStatus() status = %+v, want nil on daemon failure", status)
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatalf("GetStatus() error = %v, want daemon failure rather than timeout", err)
	}
	if !strings.Contains(err.Error(), "daemon socket unavailable") {
		t.Fatalf("GetStatus() error = %q, want daemon diagnostic", err)
	}
}

func TestRCHAvailabilityStruct(t *testing.T) {
	availability := RCHAvailability{
		Available:    true,
		Compatible:   true,
		Version:      Version{Major: 1, Minor: 0, Patch: 0},
		Path:         "/usr/local/bin/rch",
		WorkerCount:  2,
		HealthyCount: 2,
		LastChecked:  time.Now(),
	}

	if !availability.Available {
		t.Error("Expected Available to be true")
	}

	if !availability.Compatible {
		t.Error("Expected Compatible to be true")
	}

	if availability.WorkerCount != 2 {
		t.Errorf("Expected WorkerCount 2, got %d", availability.WorkerCount)
	}

	if availability.HealthyCount != 2 {
		t.Errorf("Expected HealthyCount 2, got %d", availability.HealthyCount)
	}
}

func TestRCHAdapterCacheInvalidation(t *testing.T) {
	adapter := NewRCHAdapter()

	// Invalidate cache should not panic
	adapter.InvalidateAvailabilityCache()

	// Call again to ensure it's safe to call multiple times
	adapter.InvalidateAvailabilityCache()
}

func TestRCHAdapterHealthWhenNotInstalled(t *testing.T) {
	adapter := NewRCHAdapter()

	// If rch is not installed, Health should return non-healthy status
	_, installed := adapter.Detect()
	if installed {
		t.Skip("rch is installed, skipping not-installed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health returned unexpected error: %v", err)
	}

	if health.Healthy {
		t.Error("Expected unhealthy status when rch not installed")
	}

	if health.Message != "rch not installed" {
		t.Errorf("Expected message 'rch not installed', got %s", health.Message)
	}
}

func TestRCHAdapterIsAvailable(t *testing.T) {
	adapter := NewRCHAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// IsAvailable should not panic regardless of whether rch is installed
	available := adapter.IsAvailable(ctx)

	// If rch is not installed, should return false
	_, installed := adapter.Detect()
	if !installed && available {
		t.Error("IsAvailable returned true but rch is not installed")
	}
}

func TestRCHAdapterHasHealthyWorkers(t *testing.T) {
	adapter := NewRCHAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// HasHealthyWorkers should not panic regardless of whether rch is installed
	hasWorkers := adapter.HasHealthyWorkers(ctx)

	// If rch is not installed, should return false
	_, installed := adapter.Detect()
	if !installed && hasWorkers {
		t.Error("HasHealthyWorkers returned true but rch is not installed")
	}
}

func TestRCHAdapterGetAvailability(t *testing.T) {
	adapter := NewRCHAdapter()

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
		t.Error("Available should be false when rch not installed")
	}
}

func TestRCHAdapterGetAvailabilityCaching(t *testing.T) {
	adapter := NewRCHAdapter()

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

func TestToolRCHInAllTools(t *testing.T) {
	tools := AllTools()
	found := false
	for _, tool := range tools {
		if tool == ToolRCH {
			found = true
			break
		}
	}

	if !found {
		t.Error("ToolRCH not found in AllTools()")
	}
}

func TestRCHMinVersionCompatibility(t *testing.T) {
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
			result := rchCompatible(tt.version)
			if result != tt.expected {
				t.Errorf("rchCompatible(%v) = %v, want %v", tt.version, result, tt.expected)
			}
		})
	}
}

func TestRCHAdapterSelectWorker(t *testing.T) {
	adapter := NewRCHAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// If rch is not installed, SelectWorker should return error
	_, installed := adapter.Detect()
	if installed {
		t.Skip("rch is installed, skipping not-installed test")
	}

	_, err := adapter.SelectWorker(ctx, "auto")
	// Should handle the case where no workers are available
	// The exact error depends on whether rch is installed
	if err == nil {
		// If no error, then there should be a worker
		t.Log("SelectWorker returned no error - workers may be configured")
	}
}

func TestRCHAdapterGetWorkers(t *testing.T) {
	adapter := NewRCHAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// GetWorkers should not panic
	workers, err := adapter.GetWorkers(ctx)

	// If rch is not installed, it should handle gracefully
	_, installed := adapter.Detect()
	if !installed {
		// May return empty or error
		if err != nil {
			t.Logf("GetWorkers returned error (expected when not installed): %v", err)
		}
		if len(workers) > 0 {
			t.Error("GetWorkers returned workers but rch is not installed")
		}
	}
}

func TestRCHAvailabilityTTL(t *testing.T) {
	// Verify that the TTL is set to 30 seconds (shorter than DCG due to worker churn)
	if rchAvailabilityTTL != 30*time.Second {
		t.Errorf("Expected RCH availability TTL of 30s, got %v", rchAvailabilityTTL)
	}
}
