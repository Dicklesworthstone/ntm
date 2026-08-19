package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	promptsDirName  = "prompts"
	promptExtension = ".json"
)

var (
	// redactionConfig holds the global redaction config for session prompt persistence.
	// If nil, redaction is disabled.
	redactionConfig *redaction.Config
	redactionMu     sync.RWMutex
)

// SetRedactionConfig sets the global redaction config for session prompt persistence.
// Pass nil to disable redaction.
func SetRedactionConfig(cfg *redaction.Config) {
	redactionMu.Lock()
	defer redactionMu.Unlock()
	if cfg != nil {
		// bd-pmdpn: deep-copy reference-typed fields so a caller
		// mutating cfg after Set cannot reach into stored state.
		c := cfg.DeepCopy()
		redactionConfig = &c
	} else {
		redactionConfig = nil
	}
}

// GetRedactionConfig returns the current redaction config (or nil if disabled).
// Returned value is independent of the stored config — mutating its
// reference-typed fields does not leak into future Get/Set calls.
func GetRedactionConfig() *redaction.Config {
	redactionMu.RLock()
	defer redactionMu.RUnlock()
	if redactionConfig == nil {
		return nil
	}
	c := redactionConfig.DeepCopy()
	return &c
}

// PromptEntry represents a saved prompt that was sent to agents.
type PromptEntry struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Session   string    `json:"session"`
	Content   string    `json:"content"`
	Targets   []string  `json:"targets"` // Pane IDs or "all"
	Source    string    `json:"source"`  // "cli", "template", "file"
	Template  string    `json:"template,omitempty"`
	FilePath  string    `json:"file_path,omitempty"`
}

// PromptHistory contains all prompts for a session.
type PromptHistory struct {
	Session  string        `json:"session"`
	Prompts  []PromptEntry `json:"prompts"`
	UpdateAt time.Time     `json:"updated_at"`
}

// SessionDir returns the path to the session-specific directory.
// Creates the directory if it doesn't exist.
func SessionDir(sessionName string) (string, error) {
	ntmDir, err := util.NTMDir()
	if err != nil {
		return "", err
	}

	dir := filepath.Join(ntmDir, "sessions", sanitizeFilename(sessionName))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create session directory: %w", err)
	}

	return dir, nil
}

// promptsFilePath returns the path to the prompts file for a session.
func promptsFilePath(sessionName string) (string, error) {
	dir, err := SessionDir(sessionName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "prompts.json"), nil
}

// SavePrompt saves a prompt to the session's prompt history.
func SavePrompt(entry PromptEntry) error {
	if entry.Session == "" {
		return fmt.Errorf("session name is required")
	}

	// Check privacy mode before persisting.
	if err := privacy.GetDefaultManager().CanPersist(entry.Session, privacy.OpPromptHistory); err != nil {
		// Silently skip persistence in privacy mode (don't propagate error)
		if privacy.IsPrivacyError(err) {
			return nil
		}
		return err
	}

	// Generate ID if not set
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	// Set timestamp if not set
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	// Keep the read-modify-write sequence atomic across both goroutines and
	// ntm processes. AtomicWriteFile prevents partial files, but cannot prevent
	// simultaneous writers from each replacing the other's appended history.
	unlock, err := acquireLock()
	if err != nil {
		return fmt.Errorf("lock prompt history: %w", err)
	}
	defer unlock()

	// Load existing history
	history, err := LoadPromptHistory(entry.Session)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if history == nil {
		history = &PromptHistory{
			Session: entry.Session,
			Prompts: []PromptEntry{},
		}
	}

	// Append new entry
	history.Prompts = append(history.Prompts, entry)
	history.UpdateAt = time.Now()

	// Save history
	return savePromptHistory(history)
}

// LoadPromptHistory loads the prompt history for a session.
func LoadPromptHistory(sessionName string) (*PromptHistory, error) {
	path, err := promptsFilePath(sessionName)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PromptHistory{
				Session: sessionName,
				Prompts: []PromptEntry{},
			}, nil
		}
		return nil, fmt.Errorf("failed to read prompts file: %w", err)
	}

	var history PromptHistory
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, fmt.Errorf("failed to parse prompts file: %w", err)
	}

	return &history, nil
}

// savePromptHistory writes the prompt history to disk.
func savePromptHistory(history *PromptHistory) error {
	if history == nil {
		return fmt.Errorf("prompt history is required")
	}
	// Check privacy mode before persisting.
	if history.Session != "" {
		if err := privacy.GetDefaultManager().CanPersist(history.Session, privacy.OpPromptHistory); err != nil {
			if privacy.IsPrivacyError(err) {
				return nil
			}
			return err
		}
	}

	path, err := promptsFilePath(history.Session)
	if err != nil {
		return err
	}

	historyToWrite := redactPromptHistoryForPersistence(history)

	data, err := json.MarshalIndent(historyToWrite, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize prompt history: %w", err)
	}

	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing prompts file: %w", err)
	}

	return nil
}

func redactPromptHistoryForPersistence(history *PromptHistory) *PromptHistory {
	redactionMu.RLock()
	cfg := redactionConfig
	redactionMu.RUnlock()

	if cfg == nil || cfg.Mode == redaction.ModeOff {
		return history
	}

	// For persistence, treat warn and block as "redact" so secrets are not written to disk.
	cfgCopy := *cfg
	if cfgCopy.Mode == redaction.ModeWarn || cfgCopy.Mode == redaction.ModeBlock {
		cfgCopy.Mode = redaction.ModeRedact
	}

	redacted := *history
	redacted.Prompts = make([]PromptEntry, 0, len(history.Prompts))
	for _, entry := range history.Prompts {
		redactedEntry := entry
		result := redaction.ScanAndRedact(redactedEntry.Content, cfgCopy)
		redactedEntry.Content = result.Output
		redacted.Prompts = append(redacted.Prompts, redactedEntry)
	}
	return &redacted
}
