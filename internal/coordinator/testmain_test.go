package coordinator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
)

var (
	testSessionCounter atomic.Uint64
	testPaneCounter    atomic.Uint64
)

func TestMain(m *testing.M) {
	if os.Getenv("NTM_COORDINATOR_TEST_ISOLATED") == "1" {
		os.Exit(m.Run())
	}

	isolatedDir, err := os.MkdirTemp("", "ntm-coordinator-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate coordinator tests: %v\n", err)
		os.Exit(1)
	}

	homeDir := filepath.Join(isolatedDir, "home")
	configDir := filepath.Join(isolatedDir, "config")
	dataDir := filepath.Join(isolatedDir, "data")
	stateDir := filepath.Join(isolatedDir, "state")
	for _, dir := range []string{homeDir, configDir, dataDir, stateDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			_ = os.RemoveAll(isolatedDir)
			fmt.Fprintf(os.Stderr, "create isolated coordinator dir %s: %v\n", dir, err)
			os.Exit(1)
		}
	}

	type savedEnv struct {
		key string
		val string
		ok  bool
	}
	envOverrides := []struct {
		key string
		val string
	}{
		{"HOME", homeDir},
		{"XDG_CONFIG_HOME", configDir},
		{"XDG_DATA_HOME", dataDir},
		{"XDG_STATE_HOME", stateDir},
		{"NTM_COORDINATOR_TEST_ISOLATED", "1"},
	}
	saved := make([]savedEnv, 0, len(envOverrides))
	for _, ov := range envOverrides {
		val, ok := os.LookupEnv(ov.key)
		saved = append(saved, savedEnv{key: ov.key, val: val, ok: ok})
		if err := os.Setenv(ov.key, ov.val); err != nil {
			_ = os.RemoveAll(isolatedDir)
			fmt.Fprintf(os.Stderr, "set %s for coordinator tests: %v\n", ov.key, err)
			os.Exit(1)
		}
	}

	origPendingStore := ctxmon.DefaultPendingRotationStore
	ctxmon.DefaultPendingRotationStore = ctxmon.NewPendingRotationStoreWithPath(filepath.Join(isolatedDir, "pending.jsonl"))

	code := m.Run()

	ctxmon.DefaultPendingRotationStore = origPendingStore
	for _, s := range saved {
		if s.ok {
			_ = os.Setenv(s.key, s.val)
		} else {
			_ = os.Unsetenv(s.key)
		}
	}
	if err := os.RemoveAll(isolatedDir); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated coordinator temp dir: %v\n", err)
		code = 1
	}

	os.Exit(code)
}

// testSessionName returns a unique, tmux-valid session name for parallel tests
// so per-session assignment stores and Agent Mail registries never collide.
func testSessionName(t *testing.T, prefix string) string {
	t.Helper()
	var b strings.Builder
	b.Grow(len(prefix))
	for _, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	clean := strings.Trim(b.String(), "-")
	if clean == "" {
		clean = "coord"
	}
	return fmt.Sprintf("%s-%d", clean, testSessionCounter.Add(1))
}

// testPaneID returns a unique canonical tmux pane ID (%<num>) for parallel
// tests so process-wide lifecycle coordinator leases never collide.
func testPaneID(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%%%d", 10000+testPaneCounter.Add(1))
}
