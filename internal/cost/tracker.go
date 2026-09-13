// Package cost provides API cost tracking for AI agent sessions.
package cost

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tokens"
)

// ModelPricing defines the cost per 1K tokens for input and output.
type ModelPricing struct {
	InputPer1K  float64 `json:"input_per_1k"`
	OutputPer1K float64 `json:"output_per_1k"`
}

// modelPricing contains pricing data for known models (USD per 1K tokens).
// Updated as of May 2025.
var modelPricing = map[string]ModelPricing{
	// Claude models
	"claude-opus":       {InputPer1K: 0.015, OutputPer1K: 0.075},
	"claude-opus-4":     {InputPer1K: 0.015, OutputPer1K: 0.075},
	"claude-opus-4-6":   {InputPer1K: 0.015, OutputPer1K: 0.075},
	"claude-opus-4-5":   {InputPer1K: 0.015, OutputPer1K: 0.075},
	"claude-sonnet":     {InputPer1K: 0.003, OutputPer1K: 0.015},
	"claude-sonnet-4":   {InputPer1K: 0.003, OutputPer1K: 0.015},
	"claude-haiku":      {InputPer1K: 0.00025, OutputPer1K: 0.00125},
	"claude-haiku-3-5":  {InputPer1K: 0.00025, OutputPer1K: 0.00125},
	"claude-3-opus":     {InputPer1K: 0.015, OutputPer1K: 0.075},
	"claude-3-sonnet":   {InputPer1K: 0.003, OutputPer1K: 0.015},
	"claude-3-haiku":    {InputPer1K: 0.00025, OutputPer1K: 0.00125},
	"claude-3-5-sonnet": {InputPer1K: 0.003, OutputPer1K: 0.015},
	"claude-3-5-haiku":  {InputPer1K: 0.00025, OutputPer1K: 0.00125},

	// OpenAI models
	"gpt-4o":        {InputPer1K: 0.005, OutputPer1K: 0.015},
	"gpt-4o-mini":   {InputPer1K: 0.00015, OutputPer1K: 0.0006},
	"gpt-4-turbo":   {InputPer1K: 0.01, OutputPer1K: 0.03},
	"gpt-4":         {InputPer1K: 0.03, OutputPer1K: 0.06},
	"gpt-5.5":       {InputPer1K: 0.005, OutputPer1K: 0.03},
	"gpt-5.3-codex": {InputPer1K: 0.00175, OutputPer1K: 0.014},
	"o1":            {InputPer1K: 0.015, OutputPer1K: 0.06},
	"o1-mini":       {InputPer1K: 0.003, OutputPer1K: 0.012},
	"o1-preview":    {InputPer1K: 0.015, OutputPer1K: 0.06},

	// Google models
	"gemini-pro":           {InputPer1K: 0.00025, OutputPer1K: 0.0005},
	"gemini-pro-1.5":       {InputPer1K: 0.00025, OutputPer1K: 0.0005},
	"gemini-ultra":         {InputPer1K: 0.00125, OutputPer1K: 0.00375},
	"gemini-flash":         {InputPer1K: 0.000075, OutputPer1K: 0.0003},
	"gemini-flash-1.5":     {InputPer1K: 0.000075, OutputPer1K: 0.0003},
	"gemini-2.0-flash":     {InputPer1K: 0.000075, OutputPer1K: 0.0003},
	"gemini-3-pro-preview": {InputPer1K: 0.00125, OutputPer1K: 0.00375},

	// Default fallback
	"default": {InputPer1K: 0.003, OutputPer1K: 0.015},
}

var modelDateSuffixRegex = regexp.MustCompile(`-\d{8}$`)

func normalizeModelName(model string) string {
	model = strings.TrimSpace(strings.ToLower(model))
	model = modelDateSuffixRegex.ReplaceAllString(model, "")
	return model
}

// PricingMatch describes how a price was resolved, because "we have a number"
// and "we know this model's price" are different claims.
type PricingMatch int

const (
	// PricingUnknown means nothing matched and the default row was used. The
	// amount is a guessed rate, not this model's price.
	PricingUnknown PricingMatch = iota
	// PricingFamily means a shorter family prefix matched — claude-opus-5
	// priced from the claude-opus row. The family's rate is a reasonable
	// stand-in but was never verified for this model, and a new generation
	// routinely reprices.
	PricingFamily
	// PricingExact means this model is in the table by name.
	PricingExact
)

// Known reports whether the price came from an entry for this model or its
// family, as opposed to the default row.
func (m PricingMatch) Known() bool { return m != PricingUnknown }

// PricingTableVintage is when the price table below was last revised. Surface
// it anywhere prices are shown: it is over a year old, and models released
// since are priced by family prefix or by the default row rather than by any
// figure anyone checked.
const PricingTableVintage = "May 2025"

// GetModelPricingInfo returns the pricing for a model and how it was resolved.
//
// Anything other than PricingExact means the caller is holding a rate that was
// never verified for this model: gpt-6-astra and gemini-3.1-pro-preview fall to
// the default row (which overstates Gemini severalfold), and claude-opus-5 is
// priced from the claude-opus family entry. Callers that show money must
// surface the difference rather than presenting any of it as this model's
// price.
func GetModelPricingInfo(model string) (ModelPricing, PricingMatch) {
	if pricing, ok := modelPricing[model]; ok && model != "default" {
		return pricing, PricingExact
	}

	normalized := normalizeModelName(model)
	if pricing, ok := modelPricing[normalized]; ok && normalized != "default" {
		return pricing, PricingExact
	}

	// Prefix match for variants (longest key first).
	keys := make([]string, 0, len(modelPricing))
	for key := range modelPricing {
		if key == "default" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})
	for _, key := range keys {
		if strings.HasPrefix(normalized, key) {
			return modelPricing[key], PricingFamily
		}
	}

	if pricing, ok := modelPricing["default"]; ok {
		return pricing, PricingUnknown
	}
	return ModelPricing{}, PricingUnknown
}

// EstimateTokens estimates the token count for text.
// Uses the heuristics from the internal/tokens package.
func EstimateTokens(text string) int {
	return tokens.EstimateTokens(text)
}

// FormatCost formats a USD amount as a string. Use it for amounts that came
// from a provider; for locally estimated amounts use FormatCostEstimate.
func FormatCost(usd float64) string {
	if usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	if usd < 1.0 {
		return fmt.Sprintf("$%.3f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// FormatCostEstimate formats a locally estimated USD amount.
//
// Estimates come from a ~3.5-chars-per-token count of scraped pane output
// multiplied by a price table, so the input is accurate to roughly a factor of
// two. Rendering that as "$0.0137" claims precision to a hundredth of a cent
// that the number does not have, so estimates carry a "~" and stop at cents,
// and anything under a cent says so rather than inventing digits.
func FormatCostEstimate(usd float64) string {
	if usd <= 0 {
		return "~$0"
	}
	if usd < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("~$%.2f", usd)
}
