package session

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestMain(m *testing.M) {
	tmux.DefaultClient.MaxSendDelay = 2 * time.Millisecond
	tmux.PaneProcessStartPoll = 5 * time.Millisecond
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
