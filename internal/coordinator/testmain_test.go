package coordinator

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestMain isolates os.UserConfigDir() and os.UserHomeDir() into a
// process-private root so tests that persist Agent Mail session registries
// (via internal/agentmail) never write into the developer's real
// ~/.config/ntm/sessions/.
func TestMain(m *testing.M) {
	cleanupConfig, err := testutil.IsolateUserConfigProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate coordinator config: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := cleanupConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated coordinator config: %v\n", err)
		code = 1
	}

	os.Exit(code)
}
