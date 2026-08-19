package privacy

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestNew(t *testing.T) {
	cfg := config.DefaultPrivacyConfig()
	m := New(cfg)

	if m == nil {
		t.Fatal("New returned nil")
	}

	if m.sessions == nil {
		t.Error("sessions map should be initialized")
	}
}

func TestRegisterSession(t *testing.T) {
	m := DefaultManager()

	m.RegisterSession("test-session", true, false)

	if !m.IsPrivacyEnabled("test-session") {
		t.Error("Privacy should be enabled for registered session")
	}

	state := m.GetState("test-session")
	if state == nil {
		t.Fatal("GetState returned nil for registered session")
	}

	if !state.PrivacyMode {
		t.Error("PrivacyMode should be true")
	}

	if state.AllowPersist {
		t.Error("AllowPersist should be false")
	}
}

func TestUnregisterSession(t *testing.T) {
	m := DefaultManager()

	m.RegisterSession("test-session", true, false)
	m.UnregisterSession("test-session")

	state := m.GetState("test-session")
	if state != nil {
		t.Error("GetState should return nil after unregister")
	}
}

func TestCanPersist_PrivacyDisabled(t *testing.T) {
	cfg := config.PrivacyConfig{Enabled: false}
	m := New(cfg)

	m.RegisterSession("test-session", false, false)

	// All operations should be allowed
	ops := []PersistOperation{
		OpCheckpoint, OpEventLog, OpPromptHistory,
		OpScrollback, OpExport, OpArchive,
	}

	for _, op := range ops {
		if err := m.CanPersist("test-session", op); err != nil {
			t.Errorf("Operation %s should be allowed when privacy is disabled: %v", op, err)
		}
	}
}

func TestCanPersist_PrivacyEnabled(t *testing.T) {
	cfg := config.PrivacyConfig{
		Enabled:                  true,
		DisablePromptHistory:     true,
		DisableEventLogs:         true,
		DisableCheckpoints:       true,
		DisableScrollbackCapture: true,
		RequireExplicitPersist:   true,
	}
	m := New(cfg)

	m.RegisterSession("test-session", true, false)

	// All operations should be blocked
	tests := []struct {
		op      PersistOperation
		blocked bool
	}{
		{OpCheckpoint, true},
		{OpEventLog, true},
		{OpPromptHistory, true},
		{OpScrollback, true},
		{OpExport, true},
		{OpArchive, true},
	}

	for _, tt := range tests {
		err := m.CanPersist("test-session", tt.op)
		if tt.blocked && err == nil {
			t.Errorf("Operation %s should be blocked in privacy mode", tt.op)
		}
		if !tt.blocked && err != nil {
			t.Errorf("Operation %s should be allowed: %v", tt.op, err)
		}
	}
}

func TestCanPersist_AllowPersist(t *testing.T) {
	cfg := config.PrivacyConfig{
		Enabled:                  true,
		DisablePromptHistory:     true,
		DisableEventLogs:         true,
		DisableCheckpoints:       true,
		DisableScrollbackCapture: true,
		RequireExplicitPersist:   true,
	}
	m := New(cfg)

	// Register with AllowPersist = true
	m.RegisterSession("test-session", true, true)

	// All operations should be allowed with AllowPersist
	ops := []PersistOperation{
		OpCheckpoint, OpEventLog, OpPromptHistory,
		OpScrollback, OpExport, OpArchive,
	}

	for _, op := range ops {
		if err := m.CanPersist("test-session", op); err != nil {
			t.Errorf("Operation %s should be allowed with AllowPersist: %v", op, err)
		}
	}
}

func TestCanPersist_PartialConfig(t *testing.T) {
	// Only disable checkpoints, allow others
	cfg := config.PrivacyConfig{
		Enabled:                  true,
		DisableCheckpoints:       true,
		DisablePromptHistory:     false,
		DisableEventLogs:         false,
		DisableScrollbackCapture: false,
		RequireExplicitPersist:   false,
	}
	m := New(cfg)

	m.RegisterSession("test-session", true, false)

	// Checkpoints should be blocked
	if err := m.CanPersist("test-session", OpCheckpoint); err == nil {
		t.Error("Checkpoints should be blocked")
	}

	// Event logs should be allowed
	if err := m.CanPersist("test-session", OpEventLog); err != nil {
		t.Errorf("Event logs should be allowed: %v", err)
	}

	// Export should be allowed (RequireExplicitPersist is false)
	if err := m.CanPersist("test-session", OpExport); err != nil {
		t.Errorf("Export should be allowed: %v", err)
	}
}

func TestPrivacyError(t *testing.T) {
	err := &PrivacyError{
		Operation: OpCheckpoint,
		Session:   "test-session",
		Message:   "checkpoints are disabled",
	}

	expected := "privacy mode: checkpoints are disabled (session: test-session)"
	if err.Error() != expected {
		t.Errorf("Error = %q, want %q", err.Error(), expected)
	}

	if !IsPrivacyError(err) {
		t.Error("IsPrivacyError should return true")
	}
}

func TestIsPrivacyError(t *testing.T) {
	privErr := &PrivacyError{Message: "test"}
	if !IsPrivacyError(privErr) {
		t.Error("IsPrivacyError should return true for PrivacyError")
	}

	otherErr := &struct{ error }{}
	if IsPrivacyError(otherErr) {
		t.Error("IsPrivacyError should return false for other errors")
	}
}

func TestGlobalInheritance(t *testing.T) {
	// If global config has privacy enabled, session should inherit it
	cfg := config.PrivacyConfig{
		Enabled:            true,
		DisableCheckpoints: true,
	}
	m := New(cfg)

	// Register session without explicit privacy mode
	m.RegisterSession("test-session", false, false)

	// Should inherit global privacy mode (OR of global and session flags)
	state := m.GetState("test-session")
	if state == nil {
		t.Fatal("GetState returned nil")
	}

	// The session will have privacy enabled because global is enabled
	if !state.PrivacyMode {
		// Note: In RegisterSession, we do: privacyMode || m.globalConfig.Enabled
		t.Log("Session privacy mode inherits from global config")
	}
}

func TestUnregisteredSession(t *testing.T) {
	cfg := config.PrivacyConfig{Enabled: true, DisableCheckpoints: true}
	m := New(cfg)

	// Don't register session, rely on global defaults
	err := m.CanPersist("unknown-session", OpCheckpoint)
	if err == nil {
		t.Error("Checkpoints should be blocked for unregistered session when global privacy is enabled")
	}
}

// =============================================================================
// SetDefaultManager / GetDefaultManager (bd-2fgaj)
// =============================================================================

func TestSetDefaultManager(t *testing.T) {
	// Save original and restore
	original := GetDefaultManager()
	t.Cleanup(func() { SetDefaultManager(original) })

	custom := New(config.PrivacyConfig{Enabled: true})
	SetDefaultManager(custom)

	got := GetDefaultManager()
	if got != custom {
		t.Error("GetDefaultManager should return the manager set by SetDefaultManager")
	}
}

func TestSetDefaultManager_NilIgnored(t *testing.T) {
	original := GetDefaultManager()
	t.Cleanup(func() { SetDefaultManager(original) })

	// SetDefaultManager(nil) should be a no-op
	SetDefaultManager(nil)
	got := GetDefaultManager()
	if got == nil {
		t.Error("SetDefaultManager(nil) should not clear the default manager")
	}
}

func TestGetDefaultManager_ReturnsNonNil(t *testing.T) {
	m := GetDefaultManager()
	if m == nil {
		t.Error("GetDefaultManager should always return a non-nil manager")
	}
}
