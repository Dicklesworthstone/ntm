package context

import (
	"encoding/json"
	"encoding/xml"
	"sort"
	"strings"
	"unicode/utf8"
)

// tokenByteBudget uses the pack's existing estimate, not a model tokenizer.
// Saturate before multiplying so invalid or extreme budgets cannot overflow.
func tokenByteBudget(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if tokens > maxInt/4 {
		return maxInt
	}
	return tokens * 4
}

func utf8Prefix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(text) {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func truncateTextBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	marker := "\n[truncated]"
	if len(marker) > limit {
		marker = "."
	}
	return utf8Prefix(text, limit-len(marker)) + marker
}

// truncateJSON always respects the byte budget. A zero budget omits the data;
// every nonempty result is valid JSON, including for malformed tool responses.
func truncateJSON(data json.RawMessage, tokenBudget int) json.RawMessage {
	limit := tokenByteBudget(tokenBudget)
	if limit == 0 || len(data) == 0 {
		return nil
	}
	if len(data) <= limit && json.Valid(data) {
		return data
	}
	fallback := json.RawMessage(`{"_truncated":true}`)
	if len(fallback) > limit {
		fallback = json.RawMessage(`null`)
	}
	if !json.Valid(data) {
		return fallback
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err == nil && arr != nil {
		lo, hi := 0, len(arr)
		for lo < hi {
			mid := lo + (hi-lo+1)/2
			candidate, _ := json.Marshal(arr[:mid])
			if len(candidate) <= limit {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		if lo > 0 {
			result, _ := json.Marshal(arr[:lo])
			return result
		}
		return fallback
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err == nil && obj != nil {
		truncated := map[string]json.RawMessage{"_truncated": json.RawMessage(`true`)}
		if len(fallback) > limit || string(fallback) == "null" {
			return fallback
		}
		// Stable ordering prevents arbitrary fields changing between builds.
		keys := make([]string, 0, len(obj))
		for key := range obj {
			if key != "_truncated" && key != "_original_size" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			truncated[key] = obj[key]
			candidate, _ := json.Marshal(truncated)
			if len(candidate) > limit {
				delete(truncated, key)
				// A large early field must not hide smaller later fields.
			}
		}
		result, _ := json.Marshal(truncated)
		return result
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		// JSON escaping can expand characters by six bytes. Measure the encoded
		// candidate rather than truncating by the unquoted string's length.
		lo, hi := 0, len(text)
		best := json.RawMessage(`""`)
		for lo <= hi {
			mid := lo + (hi-lo)/2
			candidate, _ := json.Marshal(truncateTextBytes(text, mid))
			if len(candidate) <= limit {
				best = candidate
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		return best
	}
	return fallback
}

func escapeXMLText(text string) string {
	var out strings.Builder
	_ = xml.EscapeText(&out, []byte(text))
	// This helper is for element character data, not attributes. Quotes are
	// safe here and keeping them literal preserves readable JSON payloads.
	return strings.NewReplacer("&#34;", "\"", "&#39;", "'").Replace(out.String())
}
