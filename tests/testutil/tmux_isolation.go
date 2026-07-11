package testutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const isolatedTmuxMarker = "NTM_TEST_TMUX_ISOLATED"

// SetupIsolatedTmuxTestProcess moves the current test process and all of its
// children onto a fresh private tmux server. The returned cleanup proves that
// the host's default-server inventory did not change.
func SetupIsolatedTmuxTestProcess() (func() error, error) {
	hostEnv := append([]string(nil), os.Environ()...)
	before := defaultTmuxInventory(hostEnv)

	root, err := os.MkdirTemp("", "ntm-test-tmux-")
	if err != nil {
		return nil, fmt.Errorf("create private TMUX_TMPDIR: %w", err)
	}
	if err := os.Unsetenv("TMUX"); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("unset inherited TMUX: %w", err)
	}
	if err := os.Setenv("TMUX_TMPDIR", root); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("set private TMUX_TMPDIR: %w", err)
	}
	if err := os.Setenv(isolatedTmuxMarker, "1"); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("mark isolated tmux test process: %w", err)
	}

	cleanup := func() error {
		after := defaultTmuxInventory(hostEnv)
		_ = os.RemoveAll(root)
		if !bytes.Equal(before, after) {
			return fmt.Errorf("host default tmux inventory changed\nbefore:\n%s\nafter:\n%s", before, after)
		}
		return nil
	}
	return cleanup, nil
}

func isolatedTmuxReady() bool {
	return os.Getenv(isolatedTmuxMarker) == "1" &&
		os.Getenv("TMUX") == "" && os.Getenv("TMUX_TMPDIR") != ""
}

func defaultTmuxInventory(env []string) []byte {
	cmd := exec.Command(tmux.BinaryPath(), "list-sessions", "-F", "#{session_name}")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	lines := strings.Fields(string(out))
	sort.Strings(lines)
	return []byte(fmt.Sprintf("exit=%v\n%s\n", err, strings.Join(lines, "\n")))
}
