package dcg

import (
	"encoding/json"
	"testing"
)

func TestDefaultDCGHookOptions(t *testing.T) {
	opts := DefaultDCGHookOptions()

	if opts.Timeout != 5 {
		t.Errorf("Expected Timeout 5, got %d", opts.Timeout)
	}

	if opts.BinaryPath != "" {
		t.Errorf("Expected empty BinaryPath, got %q", opts.BinaryPath)
	}
}

func TestGenerateHookConfig_Basic(t *testing.T) {
	opts := DefaultDCGHookOptions()
	config, err := GenerateHookConfig(opts)
	if err != nil {
		t.Fatalf("GenerateHookConfig failed: %v", err)
	}

	if len(config.Hooks.PreToolUse) != 1 {
		t.Fatalf("Expected 1 PreToolUse hook, got %d", len(config.Hooks.PreToolUse))
	}

	hook := config.Hooks.PreToolUse[0]
	handler := singleHookHandler(t, hook)

	if hook.Matcher != "Bash" {
		t.Errorf("Expected Matcher 'Bash', got %q", hook.Matcher)
	}

	if handler.Type != "command" {
		t.Errorf("Expected handler type 'command', got %q", handler.Type)
	}

	if handler.Timeout != 5 {
		t.Errorf("Expected Timeout 5, got %d", handler.Timeout)
	}

	if handler.Command == "" {
		t.Error("Expected non-empty command")
	}

	if !contains(handler.Command, "dcg") {
		t.Errorf("Command should contain 'dcg', got %q", handler.Command)
	}

	if contains(handler.Command, "check") {
		t.Errorf("Command should not use removed 'check' subcommand, got %q", handler.Command)
	}

	if contains(handler.Command, "CLAUDE_TOOL_INPUT_command") {
		t.Errorf("Command should use dcg hook stdin rather than obsolete env var, got %q", handler.Command)
	}
}

func TestGenerateHookConfig_CustomBinaryPath(t *testing.T) {
	opts := DCGHookOptions{
		BinaryPath: "/custom/path/to/dcg",
		Timeout:    3,
	}

	config, err := GenerateHookConfig(opts)
	if err != nil {
		t.Fatalf("GenerateHookConfig failed: %v", err)
	}

	handler := singleHookHandler(t, config.Hooks.PreToolUse[0])

	if !contains(handler.Command, "/custom/path/to/dcg") {
		t.Errorf("Command should contain custom binary path, got %q", handler.Command)
	}

	if handler.Timeout != 3 {
		t.Errorf("Expected Timeout 3, got %d", handler.Timeout)
	}
}

func TestGenerateHookConfig_RejectsUnsupportedInlineOptions(t *testing.T) {
	tests := []struct {
		name string
		opts DCGHookOptions
		want string
	}{
		{
			name: "audit log",
			opts: DCGHookOptions{AuditLog: "/var/log/dcg-audit.jsonl"},
			want: "audit_log",
		},
		{
			name: "blocklist",
			opts: DCGHookOptions{CustomBlocklist: []string{"rm -rf /", "DROP DATABASE"}},
			want: "custom_blocklist",
		},
		{
			name: "whitelist",
			opts: DCGHookOptions{CustomWhitelist: []string{"git status", "ls -la"}},
			want: "custom_whitelist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GenerateHookConfig(tt.opts)
			if err == nil {
				t.Fatal("GenerateHookConfig succeeded with unsupported inline option")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestCheckDCGAvailable_NotInstalled(t *testing.T) {
	// Use a binary path that doesn't exist
	invalidateDCGCacheForTest()
	availability := CheckDCGAvailable("/nonexistent/path/to/dcg")

	if availability.Available {
		t.Error("Expected DCG to be unavailable with nonexistent path")
	}

	if availability.Error == "" {
		t.Error("Expected error message for unavailable DCG")
	}
}

func TestShouldConfigureHooks_Disabled(t *testing.T) {
	// When DCG is disabled, should not configure hooks
	result := ShouldConfigureHooks(false, "")
	if result {
		t.Error("Should not configure hooks when DCG is disabled")
	}
}

func TestShouldConfigureHooks_EnabledButNotAvailable(t *testing.T) {
	// When DCG is enabled but not available, should not configure hooks
	invalidateDCGCacheForTest()
	result := ShouldConfigureHooks(true, "/nonexistent/dcg")
	if result {
		t.Error("Should not configure hooks when DCG is not available")
	}
}

func TestClaudeHookConfig_JSONFormat(t *testing.T) {
	// Test that the JSON format matches what Claude Code expects
	config := ClaudeHookConfig{
		Hooks: HooksSection{
			PreToolUse: []HookEntry{
				{
					Matcher: "Bash",
					Hooks: []HookHandler{
						{
							Type:    "command",
							Command: "dcg",
							Timeout: 5,
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	// Check that the JSON has the expected structure
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	hooks, ok := parsed["hooks"].(map[string]interface{})
	if !ok {
		t.Error("Expected 'hooks' key in JSON")
	}

	preToolUse, ok := hooks["PreToolUse"].([]interface{})
	if !ok {
		t.Error("Expected 'PreToolUse' array in hooks")
	}

	if len(preToolUse) != 1 {
		t.Errorf("Expected 1 hook entry, got %d", len(preToolUse))
	}

	entry, ok := preToolUse[0].(map[string]interface{})
	if !ok {
		t.Error("Expected hook entry to be an object")
	}

	if entry["matcher"] != "Bash" {
		t.Errorf("Expected matcher 'Bash', got %v", entry["matcher"])
	}

	handlers, ok := entry["hooks"].([]interface{})
	if !ok {
		t.Fatal("Expected nested 'hooks' array in PreToolUse entry")
	}
	if len(handlers) != 1 {
		t.Fatalf("Expected 1 nested hook handler, got %d", len(handlers))
	}
	handler, ok := handlers[0].(map[string]interface{})
	if !ok {
		t.Fatal("Expected hook handler to be an object")
	}
	if handler["type"] != "command" {
		t.Errorf("Expected handler type 'command', got %v", handler["type"])
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func singleHookHandler(t *testing.T, entry HookEntry) HookHandler {
	t.Helper()
	if len(entry.Hooks) != 1 {
		t.Fatalf("expected 1 nested hook handler, got %d", len(entry.Hooks))
	}
	return entry.Hooks[0]
}

// invalidateDCGCacheForTest clears the DCG availability cache between tests.
func invalidateDCGCacheForTest() {
	dcgAvailabilityMutex.Lock()
	dcgAvailabilityCache = DCGAvailability{}
	dcgAvailabilityMutex.Unlock()
}
