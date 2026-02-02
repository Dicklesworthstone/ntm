// Package privacy provides privacy mode enforcement for NTM.
// Privacy mode prevents persistence of sensitive session data when enabled.
package privacy

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// SessionState tracks per-session privacy settings.
type SessionState struct {
	PrivacyMode  bool // Whether privacy mode is enabled
	AllowPersist bool // Whether explicit persistence is allowed
}

// Manager handles privacy mode enforcement across sessions.
type Manager struct {
	globalConfig config.PrivacyConfig
	sessions     map[string]*SessionState
	mu           sync.RWMutex
}

// New creates a new privacy Manager with the given global config.
func New(cfg config.PrivacyConfig) *Manager {
	return &Manager{
		globalConfig: cfg,
		sessions:     make(map[string]*SessionState),
	}
}

// DefaultManager creates a Manager with default privacy config.
func DefaultManager() *Manager {
	return New(config.DefaultPrivacyConfig())
}

// RegisterSession registers a session with its privacy settings.
func (m *Manager) RegisterSession(session string, privacyMode, allowPersist bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session] = &SessionState{
		PrivacyMode:  privacyMode || m.globalConfig.Enabled,
		AllowPersist: allowPersist,
	}
}

// UnregisterSession removes a session from tracking.
func (m *Manager) UnregisterSession(session string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, session)
}

// GetState returns the privacy state for a session.
// Returns nil if session is not registered.
func (m *Manager) GetState(session string) *SessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[session]
}

// IsPrivacyEnabled returns true if privacy mode is enabled for the session.
// Returns the global default if session is not registered.
func (m *Manager) IsPrivacyEnabled(session string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if state, ok := m.sessions[session]; ok {
		return state.PrivacyMode
	}
	return m.globalConfig.Enabled
}

// CanPersist checks if persistence is allowed for the session.
// Returns an error explaining why if persistence is blocked.
func (m *Manager) CanPersist(session string, operation PersistOperation) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state := m.sessions[session]

	// Check if privacy mode is enabled
	privacyEnabled := m.globalConfig.Enabled
	allowPersist := false
	if state != nil {
		privacyEnabled = state.PrivacyMode
		allowPersist = state.AllowPersist
	}

	// If privacy mode is not enabled, persistence is allowed
	if !privacyEnabled {
		return nil
	}

	// If explicit persist is allowed, skip further checks
	if allowPersist {
		return nil
	}

	// Check specific operation against config
	switch operation {
	case OpCheckpoint:
		if m.globalConfig.DisableCheckpoints {
			return &PrivacyError{
				Operation: operation,
				Session:   session,
				Message:   "checkpoints are disabled in privacy mode",
			}
		}
	case OpEventLog:
		if m.globalConfig.DisableEventLogs {
			return &PrivacyError{
				Operation: operation,
				Session:   session,
				Message:   "event logging is disabled in privacy mode",
			}
		}
	case OpPromptHistory:
		if m.globalConfig.DisablePromptHistory {
			return &PrivacyError{
				Operation: operation,
				Session:   session,
				Message:   "prompt history is disabled in privacy mode",
			}
		}
	case OpScrollback:
		if m.globalConfig.DisableScrollbackCapture {
			return &PrivacyError{
				Operation: operation,
				Session:   session,
				Message:   "scrollback capture is disabled in privacy mode",
			}
		}
	case OpExport, OpArchive:
		if m.globalConfig.RequireExplicitPersist {
			return &PrivacyError{
				Operation: operation,
				Session:   session,
				Message:   "exports require --allow-persist in privacy mode",
			}
		}
	}

	return nil
}

// PersistOperation represents a type of persistence operation.
type PersistOperation string

const (
	// OpCheckpoint is a checkpoint creation operation.
	OpCheckpoint PersistOperation = "checkpoint"
	// OpEventLog is an event log write operation.
	OpEventLog PersistOperation = "event_log"
	// OpPromptHistory is a prompt history write operation.
	OpPromptHistory PersistOperation = "prompt_history"
	// OpScrollback is a scrollback capture operation.
	OpScrollback PersistOperation = "scrollback"
	// OpExport is an export operation.
	OpExport PersistOperation = "export"
	// OpArchive is an archive creation operation.
	OpArchive PersistOperation = "archive"
)

// PrivacyError is returned when an operation is blocked by privacy mode.
type PrivacyError struct {
	Operation PersistOperation
	Session   string
	Message   string
}

func (e *PrivacyError) Error() string {
	if e.Session != "" {
		return fmt.Sprintf("privacy mode: %s (session: %s)", e.Message, e.Session)
	}
	return fmt.Sprintf("privacy mode: %s", e.Message)
}

// IsPrivacyError returns true if the error is a privacy error.
func IsPrivacyError(err error) bool {
	_, ok := err.(*PrivacyError)
	return ok
}

// Global default manager instance (initialized lazily).
var (
	defaultManager atomic.Pointer[Manager]
)

// GetDefaultManager returns the global default privacy manager.
func GetDefaultManager() *Manager {
	if m := defaultManager.Load(); m != nil {
		return m
	}

	cfg := config.Default()
	m := New(cfg.Privacy)
	if defaultManager.CompareAndSwap(nil, m) {
		return m
	}
	return defaultManager.Load()
}

// SetDefaultManager sets the global default privacy manager.
// This should only be called during initialization.
func SetDefaultManager(m *Manager) {
	if m == nil {
		return
	}
	defaultManager.Store(m)
}
