package testutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const isolatedTmuxMarker = "NTM_TEST_TMUX_ISOLATED"

var isolationState struct {
	sync.RWMutex
	root string
}

// SetupIsolatedTmuxTestProcess moves the current test process and all of its
// children onto a fresh private tmux server. The returned cleanup proves that
// the host's default-server inventory did not change.
func SetupIsolatedTmuxTestProcess() (func() error, error) {
	// Host inventory must always mean the actual default server, even when the
	// test runner was launched from inside tmux or inherited another socket.
	hostEnv := filterEnv(os.Environ(), "TMUX")
	hostEnv = filterEnv(hostEnv, "TMUX_TMPDIR")
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
	setIsolationRoot(root)

	cleanup := func() error {
		after := defaultTmuxInventory(hostEnv)
		setIsolationRoot("")
		_ = os.RemoveAll(root)
		if !bytes.Equal(before, after) {
			return fmt.Errorf("host default tmux inventory changed\nbefore:\n%s\nafter:\n%s", before, after)
		}
		return nil
	}
	return cleanup, nil
}

func isolatedTmuxReady() bool {
	root := getIsolationRoot()
	if root == "" {
		return false
	}
	return os.Getenv(isolatedTmuxMarker) == "1" &&
		os.Getenv("TMUX") == "" && os.Getenv("TMUX_TMPDIR") == root
}

// isolatedTmuxCommandEnv returns an execution environment bound to the
// process-owned private root. Callers must attach it directly to every tmux or
// ntm command instead of relying on mutable process-global routing variables.
func isolatedTmuxCommandEnv() ([]string, bool) {
	root := getIsolationRoot()
	if root == "" {
		return nil, false
	}
	env := filterEnv(os.Environ(), "TMUX")
	env = filterEnv(env, "TMUX_TMPDIR")
	env = filterEnv(env, isolatedTmuxMarker)
	env = append(env, "TMUX_TMPDIR="+root, isolatedTmuxMarker+"=1")
	return env, true
}

// bindIsolatedTmuxCommand binds shared test helpers to the process-owned tmux
// root whenever they launch ntm or tmux. Other subprocesses keep their normal
// environment.
func bindIsolatedTmuxCommand(name string, cmd *exec.Cmd) error {
	base := filepath.Base(name)
	if base != "ntm" && base != filepath.Base(tmux.BinaryPath()) {
		return nil
	}
	env, ok := isolatedTmuxCommandEnv()
	if !ok {
		return fmt.Errorf("tmux-backed command %q refused: isolated test process is not configured", name)
	}
	cmd.Env = env
	return nil
}

func setIsolationRoot(root string) {
	isolationState.Lock()
	defer isolationState.Unlock()
	isolationState.root = root
}

func getIsolationRoot() string {
	isolationState.RLock()
	defer isolationState.RUnlock()
	return isolationState.root
}

func defaultTmuxInventory(env []string) []byte {
	cmd := exec.Command(tmux.BinaryPath(), "list-sessions", "-F",
		"#{session_name}\\t#{session_id}\\t#{session_created}\\t#{session_windows}\\t#{session_attached}")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	lines := strings.Fields(string(out))
	sort.Strings(lines)
	return []byte(fmt.Sprintf("exit=%v\n%s\n", err, strings.Join(lines, "\n")))
}
