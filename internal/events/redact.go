package events

import (
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

var (
	// redactionConfig holds the global redaction config for event log writes.
	// If nil, redaction is disabled.
	redactionConfig *redaction.Config
	redactionMu     sync.RWMutex
)

// SetRedactionConfig sets the global redaction config for event logging.
// Pass nil to disable redaction.
func SetRedactionConfig(cfg *redaction.Config) {
	redactionMu.Lock()
	defer redactionMu.Unlock()
	if cfg != nil {
		// Make a copy to avoid external mutation
		c := *cfg
		redactionConfig = &c
	} else {
		redactionConfig = nil
	}
}

// GetRedactionConfig returns the current redaction config (or nil if disabled).
func GetRedactionConfig() *redaction.Config {
	redactionMu.RLock()
	defer redactionMu.RUnlock()
	if redactionConfig == nil {
		return nil
	}
	// Return a copy
	c := *redactionConfig
	return &c
}

// redactString applies redaction to a string if configured.
// Returns the (potentially redacted) string.
func redactString(s string) string {
	redactionMu.RLock()
	cfg := redactionConfig
	redactionMu.RUnlock()

	if cfg == nil || cfg.Mode == redaction.ModeOff {
		return s
	}

	// For persistence, treat warn as "redact" so secrets are not written to disk.
	cfgCopy := *cfg
	if cfgCopy.Mode == redaction.ModeWarn || cfgCopy.Mode == redaction.ModeBlock {
		cfgCopy.Mode = redaction.ModeRedact
	}

	result := redaction.ScanAndRedact(s, cfgCopy)
	return result.Output
}

// RedactEvent returns a copy of the event with sensitive data redacted.
// For persistence, warn/redact/block modes all redact secrets so raw secrets never hit disk.
func RedactEvent(event *Event) *Event {
	if event == nil {
		return nil
	}

	redactionMu.RLock()
	cfg := redactionConfig
	redactionMu.RUnlock()

	if cfg == nil || cfg.Mode == redaction.ModeOff {
		return event
	}

	// Create a copy of the event
	redacted := *event

	// Redact data fields that might contain sensitive content
	if event.Data != nil {
		redacted.Data = redactDataMap(event.Data)
	}

	return &redacted
}

// redactDataMap recursively redacts string values in a data map.
// Handles nested maps and slices to ensure secrets in deeply-nested
// JSON-derived structures are caught.
func redactDataMap(data map[string]interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}

	result := make(map[string]interface{}, len(data))
	for k, v := range data {
		result[k] = redactValue(v)
	}
	return result
}

// redactValue recursively redacts a single value (string, map, or slice).
func redactValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return redactString(val)
	case map[string]interface{}:
		return redactDataMap(val)
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, elem := range val {
			out[i] = redactValue(elem)
		}
		return out
	default:
		return v
	}
}

// RedactionSummary represents a summary of redaction findings for event logging.
// Use this instead of logging raw secrets.
type RedactionSummary struct {
	FindingsCount int            `json:"findings_count"`
	Categories    map[string]int `json:"categories,omitempty"`
	Action        string         `json:"action"` // "warn", "redact", "block"
}

// SummarizeRedaction creates a RedactionSummary from a redaction.Result.
// This is safe to log - it contains counts, not actual secrets.
func SummarizeRedaction(result redaction.Result) RedactionSummary {
	summary := RedactionSummary{
		FindingsCount: len(result.Findings),
		Action:        string(result.Mode),
	}

	if len(result.Findings) > 0 {
		summary.Categories = make(map[string]int)
		for _, f := range result.Findings {
			summary.Categories[string(f.Category)]++
		}
	}

	return summary
}
