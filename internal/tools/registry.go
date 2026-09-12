package tools

import (
	"context"
	"sync"
)

// Registry maintains a collection of tool adapters
type Registry struct {
	mu       sync.RWMutex
	adapters map[ToolName]Adapter
}

// HealthReport summarizes tool health across the registry.
type HealthReport struct {
	Total     int `json:"total"`
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
	Missing   int `json:"missing"`
	// Disabled counts tools the configuration turned off, which are never
	// probed and so are neither healthy, unhealthy, nor known-missing
	// (ntm#313). Total = Healthy + Unhealthy + Missing + Disabled.
	Disabled int               `json:"disabled,omitempty"`
	Tools    map[ToolName]bool `json:"tools"`
}

// globalRegistry is the default registry instance
var (
	globalRegistry = &Registry{
		adapters: make(map[ToolName]Adapter),
	}
)

// Register adds an adapter to the registry
func (r *Registry) Register(adapter Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[adapter.Name()] = adapter
}

// Get returns an adapter by name
func (r *Registry) Get(name ToolName) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter, ok := r.adapters[name]
	return adapter, ok
}

// GetAllInfo returns ToolInfo for all registered tools
func (r *Registry) GetAllInfo(ctx context.Context) []*ToolInfo {
	return r.GetAllInfoExcept(ctx, nil)
}

// GetAllInfoExcept returns ToolInfo for all registered tools, reporting every
// tool named in disabled as a not-probed entry instead of running its
// detection, version, and health subprocesses.
//
// A disabled integration must not execute its binary just to fill in an
// inventory row: a `[cass] enabled = false` snapshot used to spend five
// seconds shelling out to `cass health --json` anyway (ntm#313). A nil or
// empty disabled map probes everything, which is the default.
func (r *Registry) GetAllInfoExcept(ctx context.Context, disabled map[ToolName]bool) []*ToolInfo {
	r.mu.RLock()
	adapters := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		adapters = append(adapters, a)
	}
	r.mu.RUnlock()

	infos := make([]*ToolInfo, 0, len(adapters))
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Disabled tools are recorded FIRST, before any probing goroutine exists.
	// Appending them inside the spawn loop instead would have this goroutine
	// writing the shared slice while probe goroutines write it under the
	// mutex — a data race, and the caller sorts the result anyway, so there is
	// nothing to gain from interleaving them.
	for _, a := range adapters {
		if disabled[a.Name()] {
			infos = append(infos, DisabledToolInfo(a.Name()))
		}
	}

	for _, a := range adapters {
		if disabled[a.Name()] {
			continue // recorded above; never probed
		}
		wg.Add(1)
		go func(adapter Adapter) {
			defer wg.Done()
			info, _ := adapter.Info(ctx)
			if info != nil {
				mu.Lock()
				infos = append(infos, info)
				mu.Unlock()
			}
		}(a)
	}
	wg.Wait()

	return infos
}

// GetHealthReport returns a health summary for all registered tools
func (r *Registry) GetHealthReport(ctx context.Context) *HealthReport {
	return r.GetHealthReportExcept(ctx, nil)
}

// GetHealthReportExcept returns a health summary for all registered tools,
// skipping the probes for every tool named in disabled. Disabled tools are
// counted in Total and reported unhealthy-but-unprobed in Tools, so the
// summary's arithmetic still covers the whole registry (ntm#313).
func (r *Registry) GetHealthReportExcept(ctx context.Context, disabled map[ToolName]bool) *HealthReport {
	r.mu.RLock()
	adapters := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		adapters = append(adapters, a)
	}
	r.mu.RUnlock()

	report := &HealthReport{
		Tools: make(map[ToolName]bool),
	}
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Counted FIRST, before any probing goroutine exists: report.Tools is a
	// plain map, and writing it here while a probe goroutine writes it under
	// the mutex is a concurrent map write, which panics rather than merely
	// racing.
	for _, a := range adapters {
		if disabled[a.Name()] {
			report.Total++
			report.Disabled++
			report.Tools[a.Name()] = false
		}
	}

	for _, a := range adapters {
		if disabled[a.Name()] {
			continue // counted above; never probed
		}
		wg.Add(1)
		go func(adapter Adapter) {
			defer wg.Done()
			name := adapter.Name()

			// Check installation first (fast)
			_, installed := adapter.Detect()

			var healthy bool
			if installed {
				h, err := adapter.Health(ctx)
				healthy = err == nil && h != nil && h.Healthy
			}

			mu.Lock()
			defer mu.Unlock()

			report.Total++
			if !installed {
				report.Missing++
				report.Tools[name] = false
			} else if healthy {
				report.Healthy++
				report.Tools[name] = true
			} else {
				report.Unhealthy++
				report.Tools[name] = false
			}
		}(a)
	}
	wg.Wait()

	return report
}

// Global registry functions for convenience

// Register adds an adapter to the global registry
func Register(adapter Adapter) {
	globalRegistry.Register(adapter)
}

// Get returns an adapter from the global registry
func Get(name ToolName) (Adapter, bool) {
	return globalRegistry.Get(name)
}

// GetAllInfo returns all tool info from the global registry
func GetAllInfo(ctx context.Context) []*ToolInfo {
	return globalRegistry.GetAllInfo(ctx)
}

// GetAllInfoExcept returns all tool info from the global registry without
// probing the tools named in disabled.
func GetAllInfoExcept(ctx context.Context, disabled map[ToolName]bool) []*ToolInfo {
	return globalRegistry.GetAllInfoExcept(ctx, disabled)
}

// GetHealthReport returns health report from the global registry
func GetHealthReport(ctx context.Context) *HealthReport {
	return globalRegistry.GetHealthReport(ctx)
}

// GetHealthReportExcept returns a health report from the global registry
// without probing the tools named in disabled.
func GetHealthReportExcept(ctx context.Context, disabled map[ToolName]bool) *HealthReport {
	return globalRegistry.GetHealthReportExcept(ctx, disabled)
}
