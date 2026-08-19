package dcg

import "testing"

func TestGenerateRCHHookEntry_Basic(t *testing.T) {
	opts := RCHHookOptions{
		Patterns: []string{"^cargo build", "^go build"},
	}

	entry, err := GenerateRCHHookEntry(opts)
	if err != nil {
		t.Fatalf("GenerateRCHHookEntry failed: %v", err)
	}

	if entry.Matcher != "Bash" {
		t.Errorf("Expected matcher 'Bash', got %q", entry.Matcher)
	}

	handler := singleHookHandler(t, entry)
	if handler.Type != "command" {
		t.Errorf("Expected handler type 'command', got %q", handler.Type)
	}

	if handler.Timeout != 5 {
		t.Errorf("Expected timeout 5, got %d", handler.Timeout)
	}

	if !contains(handler.Command, "rch") {
		t.Errorf("Expected command to contain rch, got %q", handler.Command)
	}

	if contains(handler.Command, "intercept") {
		t.Errorf("Command should not use removed 'intercept' subcommand, got %q", handler.Command)
	}

	if contains(handler.Command, "CLAUDE_TOOL_INPUT_command") {
		t.Errorf("Command should use rch hook stdin rather than obsolete env var, got %q", handler.Command)
	}
}

func TestGenerateRCHHookEntry_CustomBinary(t *testing.T) {
	opts := RCHHookOptions{
		BinaryPath: "/opt/rch",
		Patterns:   []string{"^go build"},
		Timeout:    3,
	}

	entry, err := GenerateRCHHookEntry(opts)
	if err != nil {
		t.Fatalf("GenerateRCHHookEntry failed: %v", err)
	}

	handler := singleHookHandler(t, entry)
	if !contains(handler.Command, "/opt/rch") {
		t.Errorf("Expected command to include custom binary path, got %q", handler.Command)
	}

	if handler.Timeout != 3 {
		t.Errorf("Expected timeout 3, got %d", handler.Timeout)
	}
}

func TestGenerateRCHHookEntry_EmptyPatterns(t *testing.T) {
	_, err := GenerateRCHHookEntry(RCHHookOptions{})
	if err == nil {
		t.Fatal("Expected error with empty patterns, got nil")
	}
}

func TestShouldConfigureRCHHooks(t *testing.T) {
	if ShouldConfigureRCHHooks(false, []string{"^cargo build"}) {
		t.Error("Expected hooks disabled when RCH is disabled")
	}

	if ShouldConfigureRCHHooks(true, []string{}) {
		t.Error("Expected hooks disabled with empty patterns")
	}

	if !ShouldConfigureRCHHooks(true, []string{"^cargo build"}) {
		t.Error("Expected hooks enabled when RCH is enabled with patterns")
	}
}
