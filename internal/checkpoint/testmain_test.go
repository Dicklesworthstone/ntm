package checkpoint

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestMain isolates every real-tmux checkpoint test behind a private tmux
// server (private TMUX_TMPDIR socket root) and isolated git config so
// capture/restore suites can never touch sessions on the developer's default
// tmux server (#220) or read the developer's global git config.
func TestMain(m *testing.M) {
	tmux.PaneProcessStartPoll = 5 * time.Millisecond
	restoreContextReadyPoll = 5 * time.Millisecond
	tmux.DefaultClient.MaxSendDelay = 2 * time.Millisecond
	if os.Getenv("NTM_CHECKPOINT_TEST_ISOLATED") == "1" {
		os.Exit(m.Run())
	}
	cleanupTmux, err := testutil.IsolateTmuxTestProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate checkpoint tmux tests: %v\n", err)
		os.Exit(1)
	}
	cleanupGit, err := testutil.IsolateGitConfigProcess()
	if err != nil {
		_ = cleanupTmux()
		fmt.Fprintf(os.Stderr, "isolate checkpoint git config: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("NTM_CHECKPOINT_TEST_ISOLATED", "1")

	code := m.Run()

	_ = os.Unsetenv("NTM_CHECKPOINT_TEST_ISOLATED")
	if err := cleanupGit(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated checkpoint git config: %v\n", err)
		code = 1
	}
	if err := cleanupTmux(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated checkpoint tmux: %v\n", err)
		code = 1
	}

	os.Exit(code)
}
