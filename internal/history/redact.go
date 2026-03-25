package history

import (
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

var (
	// redactionConfig holds the global redaction config for history writes.
	// If nil, redaction is disabled.
	redactionConfig *redaction.Config
	redactionMu     sync.RWMutex
)

// SetRedactionConfig sets the global redaction config for history writes.
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

// redactPrompt applies redaction to a prompt if configured.
// Returns the (potentially redacted) prompt.
func redactPrompt(prompt string) string {
	redactionMu.RLock()
	cfg := redactionConfig
	redactionMu.RUnlock()

	if cfg == nil || cfg.Mode == redaction.ModeOff {
		return prompt
	}

	// For persistence, treat warn and block as "redact" so secrets are not written to disk.
	cfgCopy := *cfg
	if cfgCopy.Mode == redaction.ModeWarn || cfgCopy.Mode == redaction.ModeBlock {
		cfgCopy.Mode = redaction.ModeRedact
	}

	result := redaction.ScanAndRedact(prompt, cfgCopy)
	return result.Output
}

// RedactEntry returns a copy of the entry with the prompt redacted.
// For persistence, warn/redact/block modes all redact secrets so raw secrets never hit disk.
// Off mode returns the original entry unchanged.
func RedactEntry(entry *HistoryEntry) *HistoryEntry {
	if entry == nil {
		return nil
	}

	redactionMu.RLock()
	cfg := redactionConfig
	redactionMu.RUnlock()

	if cfg == nil || cfg.Mode == redaction.ModeOff {
		return entry
	}

	// Create a copy with redacted fields.
	// redactPrompt handles warn/redact/block modes.
	redacted := *entry
	redacted.Prompt = redactPrompt(entry.Prompt)
	if redacted.Error != "" {
		redacted.Error = redactPrompt(entry.Error)
	}
	return &redacted
}
