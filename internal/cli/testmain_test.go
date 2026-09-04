package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const (
	testAgentCatCommandTemplate = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}/bin/cat`
	// Codex panes must show the composer marker ("›") before the delivery
	// readiness probe lets a prompt through, so the codex fake prints it first.
	testAgentCodexCommandTemplate  = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}printf '\342\200\272 \n' && /bin/cat`
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
	cleanupTmux, err := testutil.IsolateTmuxTestProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate CLI tmux tests: %v\n", err)
		os.Exit(1)
	}

	// CLI tests exercise git-touching paths (hooks install via `ntm init`,
	// worktrees, ...); never let them read or write the developer's real git
	// configuration (#225).
	cleanupGit, err := testutil.IsolateGitConfigProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate CLI git config: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := cleanupGit(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated CLI git config: %v\n", err)
		code = 1
	}
	if err := cleanupTmux(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated CLI tmux: %v\n", err)
		code = 1
	}

	os.Exit(code)
}
