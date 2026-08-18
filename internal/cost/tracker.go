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

// GetModelPricing returns the pricing for a model.
// If the model is not found, returns default pricing.
func GetModelPricing(model string) ModelPricing {
	if pricing, ok := modelPricing[model]; ok {
		return pricing
	}

	normalized := normalizeModelName(model)
	if pricing, ok := modelPricing[normalized]; ok {
		return pricing
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
			return modelPricing[key]
		}
	}

	if pricing, ok := modelPricing["default"]; ok {
		return pricing
	}
	return ModelPricing{}
}

// EstimateTokens estimates the token count for text.
// Uses the heuristics from the internal/tokens package.
func EstimateTokens(text string) int {
	return tokens.EstimateTokens(text)
}

// FormatCost formats a USD amount as a string.
func FormatCost(usd float64) string {
	if usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	if usd < 1.0 {
		return fmt.Sprintf("$%.3f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}
