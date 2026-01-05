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

// globalRegistry is the default registry instance
var (
	globalRegistry = &Registry{
		adapters: make(map[ToolName]Adapter),
	}
	globalMu sync.RWMutex
)

// NewRegistry creates a new adapter registry
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[ToolName]Adapter),
	}
}

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

// All returns all registered adapters
func (r *Registry) All() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapters := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		adapters = append(adapters, a)
	}
	return adapters
}

// Detected returns all adapters for installed tools
func (r *Registry) Detected() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	detected := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		if _, installed := a.Detect(); installed {
			detected = append(detected, a)
		}
	}
	return detected
}

// Names returns the names of all registered adapters
func (r *Registry) Names() []ToolName {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]ToolName, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	return names
}

// GetAllInfo returns ToolInfo for all registered tools
func (r *Registry) GetAllInfo(ctx context.Context) []*ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]*ToolInfo, 0, len(r.adapters))
	for _, a := range r.adapters {
		info, _ := a.Info(ctx)
		if info != nil {
			infos = append(infos, info)
		}
	}
	return infos
}

// HealthReport provides a summary of all tool health states
type HealthReport struct {
	Total     int               `json:"total"`
	Healthy   int               `json:"healthy"`
	Unhealthy int               `json:"unhealthy"`
	Missing   int               `json:"missing"`
	Tools     map[ToolName]bool `json:"tools"`
}

// GetHealthReport returns a health summary for all registered tools
func (r *Registry) GetHealthReport(ctx context.Context) *HealthReport {
	r.mu.RLock()
	defer r.mu.RUnlock()

	report := &HealthReport{
		Tools: make(map[ToolName]bool),
	}

	for name, a := range r.adapters {
		report.Total++

		if _, installed := a.Detect(); !installed {
			report.Missing++
			report.Tools[name] = false
			continue
		}

		health, err := a.Health(ctx)
		if err != nil || health == nil || !health.Healthy {
			report.Unhealthy++
			report.Tools[name] = false
		} else {
			report.Healthy++
			report.Tools[name] = true
		}
	}

	return report
}

// Global registry functions for convenience

// Register adds an adapter to the global registry
func Register(adapter Adapter) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalRegistry.Register(adapter)
}

// Get returns an adapter from the global registry
func Get(name ToolName) (Adapter, bool) {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalRegistry.Get(name)
}

// GetAll returns all adapters from the global registry
func GetAll() []Adapter {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalRegistry.All()
}

// GetDetected returns all detected tools from the global registry
func GetDetected() []Adapter {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalRegistry.Detected()
}

// GetInfo returns tool info from the global registry
func GetInfo(ctx context.Context, name ToolName) (*ToolInfo, error) {
	globalMu.RLock()
	defer globalMu.RUnlock()

	adapter, ok := globalRegistry.Get(name)
	if !ok {
		return nil, ErrToolNotInstalled
	}
	return adapter.Info(ctx)
}

// GetAllInfo returns all tool info from the global registry
func GetAllInfo(ctx context.Context) []*ToolInfo {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalRegistry.GetAllInfo(ctx)
}

// GetHealthReport returns health report from the global registry
func GetHealthReport(ctx context.Context) *HealthReport {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalRegistry.GetHealthReport(ctx)
}

// GlobalRegistry returns the global registry instance
func GlobalRegistry() *Registry {
	return globalRegistry
}
