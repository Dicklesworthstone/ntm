package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestSilentExitErrorPreservesRawExitCodes(t *testing.T) {
	if err := newSilentExitError(0); err != nil {
		t.Fatalf("newSilentExitError(0) = %v, want nil", err)
	}
	for _, code := range []int{1, 2, 130} {
		err := newSilentExitError(code)
		if got := ExitCode(err); got != code {
			t.Errorf("ExitCode(newSilentExitError(%d)) = %d, want %d", code, got, code)
		}
	}

	// Verify `safety claude-hook` returns exit code 2 in-process without calling os.Exit.
	cmd := newSafetyClaudeHookCmd()
	var stderr bytes.Buffer
	cmd.SetIn(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git reset --hard HEAD~1"}}`))
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected blocked command in safety claude-hook to return non-nil error")
	}
	if got := ExitCode(err); got != claudeHookRefuse {
		t.Fatalf("safety claude-hook ExitCode = %d, want %d (stderr=%s)", got, claudeHookRefuse, stderr.String())
	}
}
