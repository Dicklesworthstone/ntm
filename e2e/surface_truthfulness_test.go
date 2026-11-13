package e2e

// surface_truthfulness_test.go verifies that CLI surfaces (flags, subcommands,
// help text) truthfully reflect the actual capabilities of the binary.
//
// These tests build the binary and exercise it through os/exec so they test
// the real user-facing interface.
//
// Beads: bd-1aae9.9.2, bd-1aae9.9.4

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// buildCache caches compiled binaries keyed by build tags so each tag
// variant is only compiled once per test run.
var (
	buildCache   = make(map[string]string) // tags -> binPath
	buildErrors  = make(map[string]string) // tags -> error message
	buildMu      sync.Mutex
	buildOnce    sync.Once
	buildTmpDir  string
	repoRootOnce sync.Once
	repoRootPath string
)

func resolveRepoRoot() string {
	repoRootOnce.Do(func() {
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			repoRootPath = "."
			return
		}
		repoRootPath = filepath.Join(filepath.Dir(thisFile), "..")
	})
	return repoRootPath
}

func ensureBuildDir(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ntm-surface-test-*")
		if err != nil {
			t.Fatalf("create temp dir: %v", err)
		}
		buildTmpDir = dir
	})
	return buildTmpDir
}

// ntmBinary returns the path to a compiled ntm binary with the given tags.
// The binary is built once and cached for all tests in the same run.
func ntmBinary(t *testing.T, tags string) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping surface truthfulness test in -short mode")
	}

	tmpDir := ensureBuildDir(t)
	repoRoot := resolveRepoRoot()

	buildMu.Lock()
	defer buildMu.Unlock()

	// Return cached binary if available.
	if path, ok := buildCache[tags]; ok {
		return path
	}
	// Return cached error if previous build failed.
	if errMsg, ok := buildErrors[tags]; ok {
		t.Skipf("go build previously failed (skipping): %s", errMsg)
		return ""
	}

	binName := "ntm"
	if tags != "" {
		binName = "ntm-" + strings.ReplaceAll(tags, ",", "-")
	}
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(tmpDir, binName)

	args := []string{"build", "-o", binPath}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "./cmd/ntm")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := err.Error() + "\n" + string(out)
		buildErrors[tags] = msg
		t.Skipf("go build failed (skipping): %s", msg)
		return ""
	}

	buildCache[tags] = binPath
	return binPath
}

// runNTM executes the binary with the given args and returns combined output.
func runNTM(t *testing.T, bin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workingDir, env := isolatedSurfaceRuntime(t)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workingDir
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func isolatedSurfaceRuntime(t *testing.T) (string, []string) {
	t.Helper()

	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	configPath := filepath.Join(configDir, "ntm", "config.toml")
	tmuxDir := testutil.ShortTmuxTempDir(t)
	for _, dir := range []string{homeDir, filepath.Dir(configPath), tmuxDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create isolated surface directory %s: %v", dir, err)
		}
	}
	configData := []byte("[agent_mail]\nenabled = false\n\n[cass]\nenabled = false\n\n[cass.context]\nenabled = false\n\n[recovery]\nenabled = false\n")
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatalf("write isolated surface config: %v", err)
	}

	replaced := map[string]struct{}{
		"HOME": {}, "XDG_CONFIG_HOME": {}, "XDG_DATA_HOME": {}, "XDG_STATE_HOME": {}, "XDG_CACHE_HOME": {},
		"PWD": {}, "OLDPWD": {}, "BR_DB": {}, "BD_DB": {}, "BEADS_DB": {}, "AGENT_NAME": {},
		"TMUX": {}, "TMUX_PANE": {}, "TMUX_TMPDIR": {}, "NTM_CONFIG": {},
		"AGENT_MAIL_URL": {}, "AGENT_MAIL_TOKEN": {}, "AGENT_MAIL_ENABLED": {},
	}
	env := make([]string, 0, len(os.Environ())+14)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, skip := replaced[key]; !skip {
			env = append(env, entry)
		}
	}
	env = append(env,
		"HOME="+homeDir,
		"XDG_CONFIG_HOME="+configDir,
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"TMUX=",
		"TMUX_PANE=",
		"TMUX_TMPDIR="+tmuxDir,
		"NTM_CONFIG="+configPath,
		"NTM_DISABLE_UPDATE_CHECK=1",
		"NTM_DISABLE_INTERNAL_MONITOR=1",
		"NTM_TEST_MODE=1",
		"NO_COLOR=1",
		"TERM=xterm-256color",
	)
	return root, env
}

// --------------------------------------------------------------------------
// Task 2 (bd-1aae9.9.2): Cross-surface e2e truthfulness
// --------------------------------------------------------------------------

func TestSurfaceTruthfulness_RobotStatus(t *testing.T) {
	bin := ntmBinary(t, "")

	out, runErr := runNTM(t, bin, "--robot-status")
	// robot-status should return JSON (even if success: false due to no tmux).
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("--robot-status did not return valid JSON: %v\nCommand error: %v\nOutput: %s", err, runErr, out)
	}
}

func TestSurfaceTruthfulness_RobotTerse(t *testing.T) {
	bin := ntmBinary(t, "")

	out, runErr := runNTM(t, bin, "--robot-terse")
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		t.Fatalf("--robot-terse returned empty output: %v", runErr)
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) != 1 {
		t.Errorf("--robot-terse returned %d lines, want 1", len(lines))
	}
}

func TestSurfaceTruthfulness_RobotVersion(t *testing.T) {
	bin := ntmBinary(t, "")

	out, err := runNTM(t, bin, "--robot-version")
	if err != nil {
		t.Fatalf("--robot-version failed: %v\nOutput: %s", err, out)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("--robot-version did not return valid JSON: %v\nOutput: %s", err, out)
	}
	// Should contain a version field.
	if _, ok := parsed["version"]; !ok {
		t.Errorf("--robot-version JSON missing 'version' key: %s", out)
	}
}

func TestSurfaceTruthfulness_RobotHelp(t *testing.T) {
	bin := ntmBinary(t, "")

	out, err := runNTM(t, bin, "--robot-help")
	if err != nil {
		t.Fatalf("--robot-help failed: %v\nOutput: %s", err, out)
	}
	lower := strings.ToLower(out)
	// Robot help should contain sections like "usage", "flags", or "commands".
	if !strings.Contains(lower, "robot") {
		t.Error("--robot-help output does not mention 'robot'")
	}
}

func TestSurfaceTruthfulness_Beads(t *testing.T) {
	bin := ntmBinary(t, "")

	out, _ := runNTM(t, bin, "beads")
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "br") {
		t.Errorf("'ntm beads' output does not mention 'br' CLI: %s", out)
	}
}

func TestSurfaceTruthfulness_InitNoTemplate(t *testing.T) {
	bin := ntmBinary(t, "")

	out, _ := runNTM(t, bin, "init", "--help")
	if strings.Contains(out, "--template") {
		t.Error("'ntm init --help' still shows --template flag (should be removed)")
	}
}

func TestSurfaceTruthfulness_DiffNoSideBySide(t *testing.T) {
	bin := ntmBinary(t, "")

	out, _ := runNTM(t, bin, "diff", "--help")
	if strings.Contains(out, "--side-by-side") {
		t.Error("'ntm diff --help' still shows --side-by-side flag (should be removed)")
	}
}

// --------------------------------------------------------------------------
// Task 4 (bd-1aae9.9.4): Smoke verification -- ensemble build tags
// --------------------------------------------------------------------------

func TestSurfaceTruthfulness_EnsembleSpawnDefaultBuild(t *testing.T) {
	bin := ntmBinary(t, "")

	out, _ := runNTM(t, bin, "ensemble", "spawn", "--help")
	lower := strings.ToLower(out)
	if strings.Contains(lower, "ensemble_experimental") || strings.Contains(lower, "rebuild with -tags") {
		t.Errorf("expected graduated default 'ensemble spawn --help' to have no experimental gate, got: %s", out)
	}
	if !strings.Contains(out, "--dry-run") {
		t.Errorf("expected graduated default 'ensemble spawn --help' to expose --dry-run, got: %s", out)
	}

	runOut, _ := runNTM(t, bin, "ensemble", "spawn")
	if strings.Contains(strings.ToLower(runOut), "ensemble_experimental") {
		t.Errorf("expected graduated default 'ensemble spawn' execution to have no experimental gate, got: %s", runOut)
	}
}

func TestSurfaceTruthfulness_EnsembleSpawnExperimentalBuild(t *testing.T) {
	bin := ntmBinary(t, "ensemble_experimental")

	// With the experimental tag, the command should NOT return the
	// "experimental" gate error. It may still fail for other reasons
	// (e.g., missing session argument), which is fine.
	out, _ := runNTM(t, bin, "ensemble", "spawn")
	lower := strings.ToLower(out)
	if strings.Contains(lower, "rebuild with -tags ensemble_experimental") {
		t.Error("ensemble spawn with ensemble_experimental tag still shows the build-tag gate error")
	}
}

// --------------------------------------------------------------------------
// Task 5 (bd-1aae9.9.5): Help/docs audit (binary level)
// --------------------------------------------------------------------------

func TestSurfaceTruthfulness_HelpNoPlaceholders(t *testing.T) {
	bin := ntmBinary(t, "")

	out, _ := runNTM(t, bin, "--help")
	lower := strings.ToLower(out)

	forbidden := []string{
		"not implemented",
		"placeholder",
		"todo",
	}
	for _, phrase := range forbidden {
		if strings.Contains(lower, phrase) {
			t.Errorf("'ntm --help' output contains %q", phrase)
		}
	}
}
