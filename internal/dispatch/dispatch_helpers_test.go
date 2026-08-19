package dispatch

import "context"

// PlanDelivery adapts the function to ProtocolPlanner (test-only adapter; the
// production adapter method was removed as dead code).
func (f ProtocolPlannerFunc) PlanDelivery(ctx context.Context, target Target, submit bool) (ProtocolPlan, error) {
	return f(ctx, target, submit)
}

// Wait adapts the function to Pacer (test-only adapter).
func (f PacerFunc) Wait(ctx context.Context, pace Pace) error {
	return f(ctx, pace)
}
