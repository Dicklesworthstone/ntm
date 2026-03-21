package cli

import (
	"testing"
)

func TestIsTTY(t *testing.T) {
	t.Parallel()
	// In test environment, we might not have a TTY
	// Just verify the function doesn't panic
	_ = isTTY()
}

func TestConfirmSimple_Integration(t *testing.T) {
	t.Parallel()
	// confirmSimple requires actual stdin input, so we just verify it compiles
	// Interactive testing is done manually or via integration tests
	_ = confirmSimple
}

func TestConfirmHuh_FunctionsExist(t *testing.T) {
	t.Parallel()
	// Verify the huh-based functions are exported and callable
	// Can't fully test without a TTY, but verify they compile
	_ = confirmHuh
	_ = confirmHuhDestructive
}
