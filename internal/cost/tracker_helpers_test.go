package cost

// Test-only helper moved out of tracker.go for the G1 dead-code gate.
//
// GetModelPricingInfo superseded this when the dashboard had to distinguish a
// model's real price from the default row: the dashboard was its only
// production caller and moved across, leaving this wrapper unreachable from
// ./cmd/ntm. It stays here so the existing pricing tests keep exercising the
// lookup by its original name.

// GetModelPricing returns the pricing for a model, ignoring whether the model
// was actually found. If it is not found, this is the default pricing.
func GetModelPricing(model string) ModelPricing {
	pricing, _ := GetModelPricingInfo(model)
	return pricing
}
