package agentmail

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestMain isolates os.UserConfigDir() and os.UserHomeDir() into a
// process-private root so tests that persist session agents and registries
// never write into the developer's real ~/.config/ntm/sessions/.
func TestMain(m *testing.M) {
	cleanupConfig, err := testutil.IsolateUserConfigProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate agentmail config: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := cleanupConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated agentmail config: %v\n", err)
		code = 1
	}

	os.Exit(code)
}
