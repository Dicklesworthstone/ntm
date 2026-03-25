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
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/models"
)

// EstimateTokens provides a rough token count estimate.
// Uses ~3.5 characters per token heuristic for English text.
// This is an ESTIMATE - actual token counts vary by model and content.
//
// The 3.5 chars/token heuristic is based on empirical observations across
// multiple tokenizers (GPT, Claude, etc.) for typical English code and prose.
func EstimateTokens(text string) int {
	return EstimateTokensFromLength(len(text))
}

// EstimateTokensFromLength provides a rough token count estimate from character count.
// Uses ~3.5 characters per token heuristic.
func EstimateTokensFromLength(length int) int {
	if length <= 0 {
		return 0
	}
	// ~3.5 chars per token for typical English text/code
	count := int(float64(length) / 3.5)
	if count == 0 {
		return 1
	}
	return count
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

	count := int(float64(len(text)) / charsPerToken)
	if count == 0 {
		return 1
	}
	return count
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

// DefaultContextLimit is used when a model isn't recognized.
// Delegates to the canonical registry in internal/models.
const DefaultContextLimit = models.DefaultContextLimit

// GetContextLimit returns the context limit for a given model identifier.
// Delegates to the canonical registry in internal/models.
func GetContextLimit(model string) int {
	return models.GetContextLimit(model)
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
	pct := 0.0
	if limit > 0 {
		pct = float64(tokens) * 100.0 / float64(limit)
	}
	return &UsageInfo{
		EstimatedTokens: tokens,
		ContextLimit:    limit,
		UsagePercent:    pct,
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

	// Check for JSON without allocating a new string via TrimSpace
	var first, last rune
	for _, r := range text {
		if !unicode.IsSpace(r) {
			first = r
			break
		}
	}
	for len(text) > 0 {
		r, size := utf8.DecodeLastRuneInString(text)
		if !unicode.IsSpace(r) {
			last = r
			break
		}
		text = text[:len(text)-size]
	}
	if (first == '{' && last == '}') || (first == '[' && last == ']') {
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
	if alphaChars > 0 && float64(codeChars)/float64(alphaChars) > 0.2 {
		return ContentCode
	}

	return ContentUnknown
}

// SmartEstimate uses content type detection to provide a better estimate.
func SmartEstimate(text string) int {
	contentType := DetectContentType(text)
	return EstimateTokensWithLanguageHint(text, contentType)
}
