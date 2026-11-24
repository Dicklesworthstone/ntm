package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

var (
	cliSchemaSnapshotOnce sync.Once
	cliSchemaSnapshotData []byte
	cliSchemaSnapshotErr  error
)

// cliSchemaSnapshot builds one migrated SQLite state.db byte image once per
// test binary so CLI tests that need fresh on-disk state DBs skip re-running
// all schema migrations while exercising the exact migrated schema.
func cliSchemaSnapshot(t testing.TB) []byte {
	t.Helper()
	cliSchemaSnapshotOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ntm-cli-schema-*")
		if err != nil {
			cliSchemaSnapshotErr = err
			return
		}
		defer os.RemoveAll(dir)
		dbPath := filepath.Join(dir, "schema.db")
		st, err := state.Open(dbPath)
		if err != nil {
			cliSchemaSnapshotErr = err
			return
		}
		if err := st.Migrate(); err != nil {
			_ = st.Close()
			cliSchemaSnapshotErr = err
			return
		}
		if err := st.Close(); err != nil {
			cliSchemaSnapshotErr = err
			return
		}
		cliSchemaSnapshotData, cliSchemaSnapshotErr = os.ReadFile(dbPath)
	})
	if cliSchemaSnapshotErr != nil {
		t.Fatalf("build CLI state schema snapshot: %v", cliSchemaSnapshotErr)
	}
	return cliSchemaSnapshotData
}

// seedCLIStateDB copies the migrated schema snapshot into dbPath when dbPath
// does not yet exist, leaving existing databases untouched.
func seedCLIStateDB(t testing.TB, dbPath string) {
	t.Helper()
	if dbPath == "" {
		dbPath = state.DefaultPath()
	}
	if _, err := os.Stat(dbPath); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir state db dir: %v", err)
	}
	if err := os.WriteFile(dbPath, cliSchemaSnapshot(t), 0o600); err != nil {
		t.Fatalf("seed state db: %v", err)
	}
}

// openFastCLIStateStore opens and migrates a fresh or existing state store at
// dbPath, pre-seeding non-existent files from cliSchemaSnapshot.
func openFastCLIStateStore(t testing.TB, dbPath string) *state.Store {
	t.Helper()
	seedCLIStateDB(t, dbPath)
	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("state.Open(%q): %v", dbPath, err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		t.Fatalf("store.Migrate(%q): %v", dbPath, err)
	}
	return store
}

const (
	testAgentCatCommandTemplate = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}/bin/cat`
	// Claude sends also require a visible composer before delivery.
	testAgentClaudeCommandTemplate = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}printf '\342\235\257 \n' && /bin/cat`
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
	if os.Getenv("NTM_PI_PANE_FIXTURE") == "1" && filepath.Base(os.Args[0]) == "pi" {
		os.Exit(runPiPaneFixture())
	}
	if os.Getenv("NTM_CLI_TEST_ISOLATED") == "1" || os.Getenv("NTM_ROBOT_CONTRACT_ARGS") != "" {
		// Re-execed helper process spawned by a parent cli.test that already
		// isolated TMUX_TMPDIR, git config, TMPDIR, and XDG directories (and
		// may have passed per-test XDG/TMUX overrides in cmd.Env).
		os.Exit(m.Run())
	}
	if len(os.Args) == 7 && os.Args[1] == "-test.run=^TestPipelineBackgroundResumeWorker$" &&
		os.Args[2] == "--" && os.Args[3] == "__pipeline-worker" && os.Args[4] == "--" {
		// This exact worker invocation inherits the parent's isolated test
		// environment. Replacing its git config and tmux directories changes
		// the effective selector invocation, correctly preventing reuse of
		// the parent's durable output receipt. The parent owns cleanup.
		os.Exit(m.Run())
	}
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

	// Isolate TMPDIR for the CLI test process so MaybeRunStartupCleanup and
	// per-test temp files never scan or mutate unrelated host temp files.
	origTMPDIR, hadTMPDIR := os.LookupEnv("TMPDIR")
	isolatedTmp, err := os.MkdirTemp("", "ntmcli-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate CLI temp dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("TMPDIR", isolatedTmp); err != nil {
		fmt.Fprintf(os.Stderr, "set isolated CLI TMPDIR: %v\n", err)
		os.Exit(1)
	}
	// Isolate XDG data/config dirs and Agent Mail URL so CLI unit tests never
	// touch the developer's live audit logs, manifests, or Agent Mail server.
	// Tests needing custom stubs override via t.Setenv.
	origDataHome, hadDataHome := os.LookupEnv("XDG_DATA_HOME")
	origConfigHome, hadConfigHome := os.LookupEnv("XDG_CONFIG_HOME")
	origMailURL, hadMailURL := os.LookupEnv("AGENT_MAIL_URL")
	_ = os.Setenv("XDG_DATA_HOME", filepath.Join(isolatedTmp, "xdg-data"))
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(isolatedTmp, "xdg-config"))
	_ = os.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
	_ = os.Setenv("NTM_CLI_TEST_ISOLATED", "1")
	pipeline.DefaultIdleDebounce = 15 * time.Millisecond
	pipeline.StageIdlePollInterval = 15 * time.Millisecond
	agentmail.AvailabilityProbeBackoff = 5 * time.Millisecond
	// Keep CLI assignment tests hermetic against live host CPU load; tests
	// asserting pressure behavior override via withAssignAllocationPressure.
	collectAssignAllocationPressure = func(context.Context) assign.AllocationPressure {
		return assign.AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 4}
	}
	pipeline.BackgroundWorkerFlags = func() ([]string, error) {
		return []string{"-test.run=^TestPipelineBackgroundResumeWorker$", "--"}, nil
	}

	code := m.Run()

	_ = os.Unsetenv("NTM_CLI_TEST_ISOLATED")

	if hadDataHome {
		_ = os.Setenv("XDG_DATA_HOME", origDataHome)
	} else {
		_ = os.Unsetenv("XDG_DATA_HOME")
	}
	if hadConfigHome {
		_ = os.Setenv("XDG_CONFIG_HOME", origConfigHome)
	} else {
		_ = os.Unsetenv("XDG_CONFIG_HOME")
	}
	if hadMailURL {
		_ = os.Setenv("AGENT_MAIL_URL", origMailURL)
	} else {
		_ = os.Unsetenv("AGENT_MAIL_URL")
	}
	if hadTMPDIR {
		_ = os.Setenv("TMPDIR", origTMPDIR)
	} else {
		_ = os.Unsetenv("TMPDIR")
	}
	if err := os.RemoveAll(isolatedTmp); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated CLI temp dir: %v\n", err)
		code = 1
	}

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
