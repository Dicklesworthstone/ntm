package cli

// config_migrate_test.go — bd-config-migrate-warning-wall-151x2.
//
// Field-incident coverage: (1) `ntm config migrate` end-to-end through the
// cobra command (human + --json + --dry-run), (2) the PersistentPreRunE
// fallback warning collapses to ONE line for dead-key load failures while
// parse errors keep the full form, and (3) the shell surfaces (`ntm shell
// zsh`, cobra's hidden `__complete`) emit ZERO stderr with a dead-key config
// selected via NTM_CONFIG — the exact .zshrc eval path from the incident.
//
// The warning/silence tests re-exec the test binary (completion_coverage_test
// precedent) so rootCmd's package-global state and os.Stderr stay isolated.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/startup"
)

// cliMigrateFixture mirrors the config-package fixture: removed-tier keys
// (v1.26.0), deprecated-tier keys (v1.28.0/bd-6otuk), dead tables, an
// array-of-tables block, live keys, and comments.
const cliMigrateFixture = `# global config
projects_base = "~/projects" # live

[tmux]
default_panes = 6
palette_key = "F6"

[rotation.dashboard]
port = 8080

[checkpoints]
interval_minutes = 5

[[accounts.claude]]
email = "a@example.com"

[alerts]
enabled = true
`

func writeCLIMigrateFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// pinSelectedConfig points selectedConfigPath() at path for the test's
// duration: other suite tests mutate the package-global cfgFile (--config)
// and never restore it, and cfgFile wins over NTM_CONFIG.
func pinSelectedConfig(t *testing.T, path string) {
	t.Helper()
	oldCfgFile := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = oldCfgFile })
	t.Setenv("NTM_CONFIG", path)
	startup.ResetConfig()
	t.Cleanup(startup.ResetConfig)
}

func TestConfigMigrateCommandEndToEnd(t *testing.T) {
	path := writeCLIMigrateFixture(t, cliMigrateFixture)
	pinSelectedConfig(t, path)

	out, err := captureStdout(t, func() error {
		return newConfigMigrateCmd().Execute()
	})
	if err != nil {
		t.Fatalf("config migrate: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "removed 4 dead key(s)") {
		t.Errorf("output missing removal count:\n%s", out)
	}
	for _, key := range []string{"tmux.palette_key", "rotation.dashboard", "checkpoints.interval_minutes", "accounts.claude"} {
		if !strings.Contains(out, key) {
			t.Errorf("output missing removed key %q:\n%s", key, out)
		}
	}
	if !strings.Contains(out, "backup written: "+path+".bak.") {
		t.Errorf("output missing backup path:\n%s", out)
	}
	if !strings.Contains(out, migrateNoBehaviorChange) {
		t.Errorf("output missing the no-behavior-change safety sentence:\n%s", out)
	}

	// Live keys and comments survive; dead keys are gone.
	migrated, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read migrated config: %v", readErr)
	}
	for _, want := range []string{`projects_base = "~/projects" # live`, "default_panes = 6", "[alerts]", "enabled = true"} {
		if !strings.Contains(string(migrated), want) {
			t.Errorf("migrated config lost live content %q:\n%s", want, migrated)
		}
	}
	for _, gone := range []string{"palette_key", "rotation.dashboard", "interval_minutes", "accounts.claude"} {
		if strings.Contains(string(migrated), gone) {
			t.Errorf("migrated config still contains dead content %q:\n%s", gone, migrated)
		}
	}

	// Second run: clean no-op.
	out2, err := captureStdout(t, func() error {
		return newConfigMigrateCmd().Execute()
	})
	if err != nil {
		t.Fatalf("second config migrate: %v", err)
	}
	if !strings.Contains(out2, "config is clean") {
		t.Errorf("second run should report a clean config:\n%s", out2)
	}
}

func TestConfigMigrateDryRunJSONEnvelope(t *testing.T) {
	path := writeCLIMigrateFixture(t, cliMigrateFixture)
	pinSelectedConfig(t, path)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	cmd := newConfigMigrateCmd()
	cmd.SetArgs([]string{"--dry-run"})
	out, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatalf("config migrate --dry-run --json: %v", err)
	}

	var envelope struct {
		Path           string `json:"path"`
		Clean          bool   `json:"clean"`
		DryRun         bool   `json:"dry_run"`
		BackupPath     string `json:"backup_path"`
		RemovedCount   int    `json:"removed_count"`
		BehaviorChange bool   `json:"behavior_change"`
		Changes        []struct {
			Key         string `json:"key"`
			Disposition string `json:"disposition"`
			Tier        string `json:"tier"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("parse --json envelope: %v\noutput:\n%s", err, out)
	}
	if envelope.Path != path || !envelope.DryRun || envelope.Clean {
		t.Errorf("envelope = %+v, want path=%q dry_run=true clean=false", envelope, path)
	}
	if envelope.RemovedCount != 4 || len(envelope.Changes) != 4 {
		t.Errorf("envelope counts = %d/%d changes, want 4/4:\n%s", envelope.RemovedCount, len(envelope.Changes), out)
	}
	if envelope.BehaviorChange {
		t.Error("behavior_change = true; migrate must be a provable no-op")
	}
	if envelope.BackupPath != "" {
		t.Errorf("dry-run backup_path = %q, want empty", envelope.BackupPath)
	}
	for _, change := range envelope.Changes {
		if change.Tier != "removed" && change.Tier != "deprecated" {
			t.Errorf("change %q has tier %q", change.Key, change.Tier)
		}
	}

	// Dry-run wrote nothing.
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	if string(after) != cliMigrateFixture {
		t.Error("--dry-run modified the config file")
	}
}

// --- child-process behavioral tests (stderr byte-cleanliness) --------------

const migrateChildModeEnv = "NTM_TEST_CONFIG_MIGRATE_CHILD_MODE"

// TestConfigMigrateChildDispatch is the child entry point: it runs rootCmd
// with the args selected by the mode env var and exits. Never runs as a test
// in the parent (the env var is unset there).
func TestConfigMigrateChildDispatch(t *testing.T) {
	mode := os.Getenv(migrateChildModeEnv)
	if mode == "" {
		t.Skip("parent process")
	}
	var args []string
	switch mode {
	case "config-show":
		args = []string{"config", "show"}
	case "shell-zsh":
		args = []string{"shell", "zsh"}
	case "complete":
		args = []string{cobra.ShellCompRequestCmd, ""}
	default:
		fmt.Fprintf(os.Stderr, "unknown child mode %q\n", mode)
		os.Exit(4)
	}
	rootCmd.SetArgs(args)
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

func runMigrateChild(t *testing.T, mode, configContents string) (stdout, stderr string) {
	t.Helper()
	configPath := writeCLIMigrateFixture(t, configContents)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	child := exec.Command(exe, "-test.run=^TestConfigMigrateChildDispatch$")
	child.Dir = t.TempDir() // neutral cwd: no project config overlay in play
	child.Env = append(os.Environ(),
		migrateChildModeEnv+"="+mode,
		"NTM_CONFIG="+configPath,
	)
	var outBuf, errBuf strings.Builder
	child.Stdout = &outBuf
	child.Stderr = &errBuf
	if err := child.Run(); err != nil {
		t.Fatalf("child (mode %s) failed: %v\nstdout:\n%s\nstderr:\n%s", mode, err, outBuf.String(), errBuf.String())
	}
	return outBuf.String(), errBuf.String()
}

// TestDeadKeyLoadWarningIsOneLine: with a dead-key config, a config-loading
// command warns with exactly ONE actionable stderr line pointing at
// `ntm config migrate` — never the ~30-line per-key disposition wall.
func TestDeadKeyLoadWarningIsOneLine(t *testing.T) {
	_, stderr := runMigrateChild(t, "config-show", cliMigrateFixture)

	lines := []string{}
	for _, line := range strings.Split(strings.TrimRight(stderr, "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("stderr = %d line(s), want exactly 1:\n%s", len(lines), stderr)
	}
	warning := lines[0]
	if !strings.HasPrefix(warning, "ntm: config has ") ||
		!strings.Contains(warning, "removed key(s) that never had an effect") ||
		!strings.Contains(warning, "run 'ntm config migrate' to clean them (backup kept)") ||
		!strings.Contains(warning, "details: ntm doctor") {
		t.Errorf("one-line warning has wrong shape: %q", warning)
	}
	// The multi-key disposition wall must be gone from the human surface.
	if strings.Contains(stderr, "delete it from your config file") {
		t.Errorf("stderr still carries per-key disposition text:\n%s", stderr)
	}
}

// TestParseErrorWarningKeepsFullForm: a genuine TOML syntax error is not a
// dead-key failure and keeps the historical full warning.
func TestParseErrorWarningKeepsFullForm(t *testing.T) {
	_, stderr := runMigrateChild(t, "config-show", "not = [valid\n")
	if !strings.Contains(stderr, "ntm: warning: config load failed") ||
		!strings.Contains(stderr, "using built-in defaults") {
		t.Errorf("parse-error warning lost its full form:\n%s", stderr)
	}
	if strings.Contains(stderr, "ntm config migrate") {
		t.Errorf("parse-error warning wrongly points at config migrate:\n%s", stderr)
	}
}

// TestShellIntegrationStderrByteClean: `ntm shell zsh` — the exact eval'd
// .zshrc surface from the field incident — must emit ZERO stderr bytes even
// with a dead-key config selected.
func TestShellIntegrationStderrByteClean(t *testing.T) {
	stdout, stderr := runMigrateChild(t, "shell-zsh", cliMigrateFixture)
	if stderr != "" {
		t.Errorf("ntm shell zsh wrote %d stderr byte(s) with a dead-key config:\n%s", len(stderr), stderr)
	}
	if !strings.Contains(stdout, "# NTM Shell Integration") {
		t.Errorf("shell integration script missing from stdout:\n%.200s", stdout)
	}
}

// TestCompletionStderrByteClean: cobra's hidden __complete command backs
// every tab-completion; it must emit no config warnings on stderr. (Cobra
// itself always prints one "Completion ended with directive:" trace line to
// stderr for every cobra CLI — the generated shell scripts discard it — so
// only that line is tolerated.)
func TestCompletionStderrByteClean(t *testing.T) {
	stdout, stderr := runMigrateChild(t, "complete", cliMigrateFixture)
	for _, line := range strings.Split(strings.TrimRight(stderr, "\n"), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "Completion ended with directive:") {
			continue
		}
		t.Errorf("__complete wrote non-cobra stderr line with a dead-key config: %q", line)
	}
	if !strings.Contains(stdout, "spawn") {
		t.Errorf("__complete produced no candidates:\n%.200s", stdout)
	}
}
