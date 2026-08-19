package hooks

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// LoadCommandHooksFromTOML — cover TOML parse error branch (83.3% → 100%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// buildEnvironment — cover final truncation branch for multi-byte chars (96.3% → 100%)
// ---------------------------------------------------------------------------

func TestBuildEnvironment_MultibyteMessageTruncation(t *testing.T) {
	t.Parallel()

	// This test covers the edge case where the truncation loop completes
	// (all rune starts <= targetLen=997) but the string still > 1000 bytes
	// due to a multi-byte character at the end.

	// Build a message: 997 ASCII chars + one 4-byte emoji = 1001 bytes
	// The emoji starts at position 997 (which is <= 997), so the loop completes.
	// After loop: len(msg) = 1001 > 1000, triggers the second truncation branch.
	msg := strings.Repeat("x", 997) + "🎉" // 997 + 4 = 1001 bytes

	cfg := &CommandHooksConfig{
		Hooks: []CommandHook{
			{Event: EventPreSend, Command: "echo ${#NTM_MESSAGE}"},
		},
	}
	exec := NewExecutor(cfg)
	execCtx := ExecutionContext{
		Message: msg,
	}
	results, _ := exec.RunHooksForEvent(context.Background(), EventPreSend, execCtx)
	if len(results) != 1 {
		t.Fatal("expected 1 result")
	}

	// The message should be truncated: msg[:997] + "..." = 1000 chars
	if !strings.Contains(results[0].Stdout, "1000") {
		t.Errorf("message should be truncated to 1000 chars, got stdout: %s", results[0].Stdout)
	}
}
