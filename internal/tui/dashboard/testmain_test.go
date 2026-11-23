package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

var attentionFeedTestMu sync.Mutex

func lockAttentionFeedForTest(t *testing.T) {
	attentionFeedTestMu.Lock()
	t.Cleanup(attentionFeedTestMu.Unlock)
}

func TestMain(m *testing.M) {
	// Many dashboard rendering helpers assume colors are enabled.
	// The repo's default environment sets NO_COLOR=1, so force colors on
	// for this package's tests to keep expectations stable.
	_ = os.Setenv("NTM_NO_COLOR", "0")
	_ = os.Setenv("NTM_THEME", "mocha")

	// Isolate HOME/XDG dirs so dashboard commands that touch state.db (such as
	// fetchMetricsCmd -> ensemble.LoadSession) never read or mutate the
	// developer's real ~/.config/ntm/state.db.
	tmpRoot, err := os.MkdirTemp("", "ntm-dashboard-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create dashboard test root: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", tmpRoot)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpRoot, "config"))
	_ = os.Setenv("XDG_DATA_HOME", filepath.Join(tmpRoot, "data"))
	ensemble.CloseDefaultStateStore()

	code := m.Run()
	ensemble.CloseDefaultStateStore()
	_ = os.RemoveAll(tmpRoot)
	os.Exit(code)
}
