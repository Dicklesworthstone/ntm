package tools

import "context"

// Test-only helpers replicating the removed production accessors.
//
// GetHealthReportExcept(ctx, nil) superseded these when ntm#313 taught the
// registry to skip probes for disabled tools: robot/tools.go switched to the
// Except form and left these two wrappers with no caller outside tests, which
// the G1 dead-code gate flags (it runs deadcode without -test). They stay here
// so the existing tests keep exercising the no-exclusions path by its original
// name without keeping unreachable code in the production build.

// GetHealthReport returns a health summary for all registered tools.
func (r *Registry) GetHealthReport(ctx context.Context) *HealthReport {
	return r.GetHealthReportExcept(ctx, nil)
}

// GetHealthReport returns a health report from the global registry.
func GetHealthReport(ctx context.Context) *HealthReport {
	return globalRegistry.GetHealthReport(ctx)
}
