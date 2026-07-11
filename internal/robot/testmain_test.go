package robot

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestMain(m *testing.M) {
	cleanup, err := testutil.SetupIsolatedTmuxTestProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmux test isolation failed: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "tmux test isolation failed: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
