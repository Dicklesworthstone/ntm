package tools

import (
	"context"
	"testing"
)

func newTestRegistry() *Registry {
	return &Registry{adapters: make(map[ToolName]Adapter)}
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := newTestRegistry()

	adapter := newMockAdapter(ToolBV, true)
	r.Register(adapter)

	got, ok := r.Get(ToolBV)
	if !ok {
		t.Fatal("Get() returned false for registered adapter")
	}

	if got.Name() != ToolBV {
		t.Errorf("Got adapter name %q, want %q", got.Name(), ToolBV)
	}
}

func TestRegistryGetNotFound(t *testing.T) {
	r := newTestRegistry()

	_, ok := r.Get(ToolBV)
	if ok {
		t.Error("Get() should return false for unregistered adapter")
	}
}

func TestRegistryGetAllInfo(t *testing.T) {
	r := newTestRegistry()

	r.Register(newMockAdapter(ToolBV, true))
	r.Register(newMockAdapter(ToolBD, false))

	ctx := context.Background()
	infos := r.GetAllInfo(ctx)

	if len(infos) != 2 {
		t.Errorf("GetAllInfo() returned %d infos, want 2", len(infos))
	}

	// Check that installed/uninstalled status is correct
	for _, info := range infos {
		switch info.Name {
		case ToolBV:
			if !info.Installed {
				t.Error("ToolBV should be installed")
			}
		case ToolBD:
			if info.Installed {
				t.Error("ToolBD should not be installed")
			}
		}
	}
}

func TestRegistryGetHealthReport(t *testing.T) {
	r := newTestRegistry()

	r.Register(newMockAdapter(ToolBV, true))
	r.Register(newMockAdapter(ToolBD, true))
	r.Register(newMockAdapter(ToolCASS, false))

	ctx := context.Background()
	report := r.GetHealthReport(ctx)

	if report.Total != 3 {
		t.Errorf("Total = %d, want 3", report.Total)
	}

	if report.Healthy != 2 {
		t.Errorf("Healthy = %d, want 2", report.Healthy)
	}

	if report.Missing != 1 {
		t.Errorf("Missing = %d, want 1", report.Missing)
	}

	// Check tool states
	if !report.Tools[ToolBV] {
		t.Error("ToolBV should be healthy")
	}
	if !report.Tools[ToolBD] {
		t.Error("ToolBD should be healthy")
	}
	if report.Tools[ToolCASS] {
		t.Error("ToolCASS should not be healthy (missing)")
	}
}

func TestGlobalRegistryFunctions(t *testing.T) {
	// Save current state to restore after test
	oldAdapters := make(map[ToolName]Adapter)
	globalRegistry.mu.Lock()
	for k, v := range globalRegistry.adapters {
		oldAdapters[k] = v
	}
	globalRegistry.adapters = make(map[ToolName]Adapter)
	globalRegistry.mu.Unlock()

	// Restore after test
	defer func() {
		globalRegistry.mu.Lock()
		globalRegistry.adapters = oldAdapters
		globalRegistry.mu.Unlock()
	}()

	// Test Register
	adapter := newMockAdapter(ToolBV, true)
	Register(adapter)

	// Test Get
	got, ok := Get(ToolBV)
	if !ok {
		t.Fatal("Get() returned false for registered adapter")
	}
	if got.Name() != ToolBV {
		t.Errorf("Got adapter name %q, want %q", got.Name(), ToolBV)
	}

	// Test GetAllInfo
	ctx := context.Background()
	allInfo := GetAllInfo(ctx)
	if len(allInfo) != 1 {
		t.Errorf("GetAllInfo() returned %d infos, want 1", len(allInfo))
	}

	// Test GetHealthReport
	report := GetHealthReport(ctx)
	if report.Total != 1 {
		t.Errorf("GetHealthReport() Total = %d, want 1", report.Total)
	}
}
