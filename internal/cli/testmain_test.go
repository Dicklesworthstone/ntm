package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const (
	testAgentCatCommandTemplate    = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}/bin/cat`
	testAgentBinCatCommandTemplate = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}/bin/cat`
)

func newTmuxIntegrationTestConfig(projectsBase string) *config.Config {
	testCfg := config.Default()
	testCfg.ProjectsBase = projectsBase

	// Generic tmux integration tests should stay focused on pane/session behavior
	// instead of shelling out to optional memory and coordination helpers.
	testCfg.AgentMail.Enabled = false
	testCfg.CASS.Context.Enabled = false
	testCfg.SessionRecovery.Enabled = false
	testCfg.SessionRecovery.AutoInjectOnSpawn = false
	testCfg.SessionRecovery.IncludeAgentMail = false
	testCfg.SessionRecovery.IncludeBeadsContext = false
	testCfg.SessionRecovery.IncludeCMMemories = false
	testCfg.GeminiSetup.AutoSelectProModel = false

	return testCfg
}

func TestMain(m *testing.M) {
	cleanup, err := testutil.SetupIsolatedTmuxTestProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmux test isolation failed: %v\n", err)
		os.Exit(1)
	}
	// Clean up any orphan test sessions from previous runs before starting.
	// This catches sessions left behind when tests are interrupted (Ctrl+C, timeout, etc.)
	testutil.KillAllTestSessionsSilent()

	code := m.Run()

	// Clean up after all tests complete
	testutil.KillAllTestSessionsSilent()
	if err := cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "tmux test isolation failed: %v\n", err)
		code = 1
	}

	os.Exit(code)
}
