// Package tokens provides rough token estimation for context usage visualization.
//
// IMPORTANT: These are ESTIMATES, not exact measurements.
// Actual token counts vary by model, tokenizer, and content type.
// The heuristics here are optimized for simplicity and broad applicability
// across different AI models rather than precision for any single model.
package tokens

import (
	"strings"
	"unicode"
)

// EstimateTokens provides a rough token count estimate.
// Uses ~3.5 characters per token heuristic for English text.
// This is an ESTIMATE - actual token counts vary by model and content.
//
// The 3.5 chars/token heuristic is based on empirical observations across
// multiple tokenizers (GPT, Claude, etc.) for typical English code and prose.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	// ~3.5 chars per token for typical English text/code
	return int(float64(len(text)) / 3.5)
}

// EstimateTokensWithLanguageHint provides a more accurate estimate based on content type.
// Different content types tokenize differently:
//   - Code tends to have more tokens per character (2.5-3 chars/token)
//   - English prose is typically ~4 chars/token
//   - JSON/structured data varies widely
func EstimateTokensWithLanguageHint(text string, hint ContentType) int {
	if text == "" {
		return 0
	}

	// Character-per-token ratios (empirically observed)
	var charsPerToken float64
	switch hint {
	case ContentCode:
		charsPerToken = 2.8 // Code has more punctuation, shorter tokens
	case ContentJSON:
		charsPerToken = 3.0 // JSON has structural characters
	case ContentMarkdown:
		charsPerToken = 3.5 // Mix of prose and formatting
	case ContentProse:
		charsPerToken = 4.0 // Natural language, longer words
	default:
		charsPerToken = 3.5 // General default
	}

	return int(float64(len(text)) / charsPerToken)
}

// ContentType hints at the type of content for better estimation
type ContentType int

const (
	// ContentUnknown is the default - uses general heuristic
	ContentUnknown ContentType = iota
	// ContentCode is source code (Go, Python, JS, etc.)
	ContentCode
	// ContentJSON is JSON or similar structured data
	ContentJSON
	// ContentMarkdown is Markdown or documentation
	ContentMarkdown
	// ContentProse is natural language text
	ContentProse
)

// EstimateWithOverhead applies overhead multiplier for hidden context.
// Overhead includes: system prompts, tool definitions, conversation structure,
// and other tokens that aren't visible in the raw text.
//
// Typical overhead multipliers:
//   - 1.2: Minimal system prompt, few tools
//   - 1.5: Standard chat with moderate tool use
//   - 2.0: Heavy tool use, complex system prompts
func EstimateWithOverhead(visibleText string, multiplier float64) int {
	visible := EstimateTokens(visibleText)
	return int(float64(visible) * multiplier)
}

// ContextLimits maps model identifiers to their approximate context limits.
// These are conservative estimates and may change as models are updated.
var ContextLimits = map[string]int{
	// Anthropic Claude models
	"opus":        200000,
	"opus-4":      200000,
	"claude-opus": 200000,
	"sonnet":      200000,
	"sonnet-4":    200000,
	"sonnet-3.5":  200000,
	"haiku":       200000,
	"haiku-3":     200000,

	// OpenAI models
	"gpt4":       128000,
	"gpt-4":      128000,
	"gpt4o":      128000,
	"gpt-4o":     128000,
	"gpt4-turbo": 128000,
	"o1":         128000,
	"o1-mini":    128000,
	"codex":      128000,

	// Google models
	"gemini":     1000000,
	"pro":        1000000,
	"gemini-pro": 1000000,
	"flash":      1000000,
	"ultra":      1000000,
}

// DefaultContextLimit is used when a model isn't recognized
const DefaultContextLimit = 128000

// GetContextLimit returns the context limit for a given model identifier.
// Returns DefaultContextLimit if the model is not recognized.
func GetContextLimit(model string) int {
	normalized := normalizeModel(model)
	if limit, ok := ContextLimits[normalized]; ok {
		return limit
	}
	return DefaultContextLimit
}

// normalizeModel converts model names to a canonical form for lookup.
// Handles common variations: "claude-3.5-sonnet" -> "sonnet-3.5"
func normalizeModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))

	// Handle Claude model naming conventions
	if strings.Contains(model, "claude") {
		if strings.Contains(model, "opus") {
			return "opus"
		}
		if strings.Contains(model, "sonnet") {
			if strings.Contains(model, "3.5") || strings.Contains(model, "35") {
				return "sonnet-3.5"
			}
			return "sonnet"
		}
		if strings.Contains(model, "haiku") {
			return "haiku"
		}
	}

	// Handle OpenAI model naming
	if strings.Contains(model, "gpt") {
		if strings.Contains(model, "4o") {
			return "gpt4o"
		}
		if strings.Contains(model, "4") {
			return "gpt4"
		}
	}

	// Handle Gemini naming
	if strings.Contains(model, "gemini") {
		if strings.Contains(model, "pro") {
			return "gemini-pro"
		}
		if strings.Contains(model, "flash") {
			return "flash"
		}
		if strings.Contains(model, "ultra") {
			return "ultra"
		}
		return "gemini"
	}

	// Return as-is for direct lookups
	return model
}

// UsagePercentage calculates what percentage of context is used.
// Returns a value between 0.0 and 100.0+ (can exceed 100 if over limit).
func UsagePercentage(tokenCount int, model string) float64 {
	limit := GetContextLimit(model)
	if limit == 0 {
		return 0
	}
	return float64(tokenCount) * 100.0 / float64(limit)
}

// UsageInfo provides human-readable context usage information
type UsageInfo struct {
	EstimatedTokens int     `json:"estimated_tokens"`
	ContextLimit    int     `json:"context_limit"`
	UsagePercent    float64 `json:"usage_percent"`
	Model           string  `json:"model"`
	IsEstimate      bool    `json:"is_estimate"` // Always true - reminder this is estimated
}

// GetUsageInfo returns comprehensive usage information for given text and model.
func GetUsageInfo(text, model string) *UsageInfo {
	tokens := SmartEstimate(text)
	limit := GetContextLimit(model)
	return &UsageInfo{
		EstimatedTokens: tokens,
		ContextLimit:    limit,
		UsagePercent:    float64(tokens) * 100.0 / float64(limit),
		Model:           model,
		IsEstimate:      true,
	}
}

// DetectContentType attempts to guess content type from the text.
// This is a simple heuristic and may not always be accurate.
func DetectContentType(text string) ContentType {
	if len(text) < 10 {
		return ContentUnknown
	}

	// Check for JSON
	trimmed := strings.TrimSpace(text)
	if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
		return ContentJSON
	}

	// Check for Markdown indicators
	// Only check first 4KB for efficiency
	scanLimit := 4096
	if len(text) < scanLimit {
		scanLimit = len(text)
	}
	head := text[:scanLimit]

	if strings.Contains(head, "```") ||
		strings.Contains(head, "# ") ||
		strings.Contains(head, "## ") ||
		strings.Contains(head, "- [") {
		return ContentMarkdown
	}

	// Count code-like characters
	var codeChars, alphaChars int
	for _, r := range head {
		if r == '{' || r == '}' || r == '(' || r == ')' || r == ';' || r == '=' {
			codeChars++
		}
		if unicode.IsLetter(r) {
			alphaChars++
		}
	}

	// High ratio of code characters suggests code
	if alphaChars > 0 && float64(codeChars)/float64(alphaChars) > 0.1 {
		return ContentCode
	}

	return ContentProse
}

// SmartEstimate uses content type detection to provide a better estimate.
func SmartEstimate(text string) int {
	contentType := DetectContentType(text)
	return EstimateTokensWithLanguageHint(text, contentType)
}
