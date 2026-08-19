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
	Total     int               `json:"total"`
	Healthy   int               `json:"healthy"`
	Unhealthy int               `json:"unhealthy"`
	Missing   int               `json:"missing"`
	Tools     map[ToolName]bool `json:"tools"`
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
	r.mu.RLock()
	adapters := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		adapters = append(adapters, a)
	}
	r.mu.RUnlock()

	infos := make([]*ToolInfo, 0, len(adapters))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, a := range adapters {
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

	for _, a := range adapters {
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

// GetHealthReport returns health report from the global registry
func GetHealthReport(ctx context.Context) *HealthReport {
	return globalRegistry.GetHealthReport(ctx)
}
