package session

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestMain(m *testing.M) {
	cleanupTmux, err := testutil.IsolateTmuxTestProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate session tmux tests: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := cleanupTmux(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup session tmux tests: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
