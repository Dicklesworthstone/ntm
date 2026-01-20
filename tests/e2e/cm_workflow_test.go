package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

type cmRule struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
}

type cmContext struct {
	Success              bool     `json:"success"`
	Task                 string   `json:"task"`
	RelevantBullets      []cmRule `json:"relevantBullets"`
	AntiPatterns         []cmRule `json:"antiPatterns"`
	HistorySnippets      []any    `json:"historySnippets"`
	SuggestedCassQueries []string `json:"suggestedCassQueries"`
}

const cmE2EScript = `#!/bin/sh
set -e

if [ "$1" = "context" ]; then
  shift
  task="$1"
  shift || true

  if [ -n "$CM_E2E_LOG" ]; then
    printf "context|%s\n" "$task" >> "$CM_E2E_LOG"
  fi

  if [ -n "$CM_E2E_STORE" ] && [ -f "$CM_E2E_STORE" ]; then
    cat "$CM_E2E_STORE"
    exit 0
  fi

  printf '{"success":true,"task":"%s","relevantBullets":[],"antiPatterns":[],"historySnippets":[],"suggestedCassQueries":[]}\n' "$task"
  exit 0
fi

if [ "$1" = "--version" ]; then
  echo "cm 0.0.0-e2e"
  exit 0
fi

echo "unsupported cm command" >&2
exit 1
`

func writeCMStore(t *testing.T, path string, task string, rules []cmRule, anti []cmRule) {
	t.Helper()

	payload := cmContext{
		Success:              true,
		Task:                 task,
		RelevantBullets:      rules,
		AntiPatterns:         anti,
		HistorySnippets:      []any{},
		SuggestedCassQueries: []string{},
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal cm store: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write cm store: %v", err)
	}
}

func writeCMBinary(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "cm")
	if err := os.WriteFile(path, []byte(cmE2EScript), 0755); err != nil {
		t.Fatalf("write cm script: %v", err)
	}
	return path
}

func writeTestConfig(t *testing.T, dir string, projectsBase string) string {
	t.Helper()

	configPath := filepath.Join(dir, "config.toml")
	config := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "cat"
codex = "cat"
gemini = "cat"

[agent_mail]
enabled = false

[cass]
enabled = false

[recovery]
enabled = true
include_agent_mail = false
include_beads_context = false
include_cm_memories = true
auto_inject_on_spawn = true
max_recovery_tokens = 2000
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}

func waitForPaneContains(t *testing.T, logger *testutil.TestLogger, session string, pane int, needle string, timeout time.Duration) string {
	t.Helper()

	var last string
	ok := testutil.AssertEventually(t, logger, timeout, 200*time.Millisecond, fmt.Sprintf("pane %s:%d contains %q", session, pane, needle), func() bool {
		content, err := testutil.CapturePane(session, pane)
		if err != nil {
			return false
		}
		last = content
		return strings.Contains(content, needle)
	})
	if !ok {
		return last
	}
	return last
}

func waitForLogContains(t *testing.T, logger *testutil.TestLogger, logPath string, needle string, timeout time.Duration) {
	t.Helper()

	testutil.AssertEventually(t, logger, timeout, 200*time.Millisecond, fmt.Sprintf("cm log contains %q", needle), func() bool {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return false
		}
		return strings.Contains(string(data), needle)
	})
}

func TestE2ECMSpawnInjectsRecoveryContext(t *testing.T) {
	testutil.E2ETestPrecheck(t)
	testutil.RequireUnix(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	binDir := t.TempDir()
	writeCMBinary(t, binDir)

	storePath := filepath.Join(t.TempDir(), "cm_store.json")
	logPath := filepath.Join(t.TempDir(), "cm_calls.log")

	rule := cmRule{ID: "guard-1", Content: "Never rm -rf /", Category: "safety"}
	writeCMStore(t, storePath, "e2e-seed", []cmRule{rule}, []cmRule{})

	pathEnv := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", pathEnv)
	t.Setenv("CM_E2E_STORE", storePath)
	t.Setenv("CM_E2E_LOG", logPath)

	projectsBase := t.TempDir()
	configPath := writeTestConfig(t, t.TempDir(), projectsBase)

	session := fmt.Sprintf("e2e_cm_spawn_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = execCommand("ntm", "--config", configPath, "kill", "-f", session)
	})

	logger.LogSection("Spawn session with CM recovery")
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "spawn", session, "--cc=1", "--json", "--safety", "--no-hooks")

	testutil.AssertSessionExists(t, logger, session)
	testutil.AssertPaneCountAtLeast(t, logger, session, 2)

	// Verify CM was called with the expected recovery task.
	expectedTask := fmt.Sprintf("context|%s: starting new coding session", session)
	waitForLogContains(t, logger, logPath, expectedTask, 5*time.Second)

	// Verify recovery context and guard rule were injected into the agent pane.
	content := waitForPaneContains(t, logger, session, 1, rule.Content, 6*time.Second)
	if !strings.Contains(content, "Session Recovery Context") {
		t.Errorf("expected recovery header in pane output, got:\n%s", content)
	}
	if !strings.Contains(content, "Key Decisions Made") {
		t.Errorf("expected guard section in pane output, got:\n%s", content)
	}
}

func TestE2ECMMemoryPersistsAcrossSessions(t *testing.T) {
	testutil.E2ETestPrecheck(t)
	testutil.RequireUnix(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	binDir := t.TempDir()
	writeCMBinary(t, binDir)

	storePath := filepath.Join(t.TempDir(), "cm_store.json")
	logPath := filepath.Join(t.TempDir(), "cm_calls.log")

	pathEnv := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", pathEnv)
	t.Setenv("CM_E2E_STORE", storePath)
	t.Setenv("CM_E2E_LOG", logPath)

	projectsBase := t.TempDir()
	configPath := writeTestConfig(t, t.TempDir(), projectsBase)

	// First session uses initial memory.
	ruleA := cmRule{ID: "mem-1", Content: "Always run go test ./...", Category: "workflow"}
	writeCMStore(t, storePath, "e2e-first", []cmRule{ruleA}, nil)

	sessionA := fmt.Sprintf("e2e_cm_mem_a_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = execCommand("ntm", "--config", configPath, "kill", "-f", sessionA)
	})

	logger.LogSection("Spawn session A with initial memory")
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "spawn", sessionA, "--cc=1", "--json", "--safety", "--no-hooks")
	waitForPaneContains(t, logger, sessionA, 1, ruleA.Content, 6*time.Second)

	// Update memory store to simulate new memory persisted.
	ruleB := cmRule{ID: "mem-2", Content: "Use log/slog for structured logging", Category: "logging"}
	writeCMStore(t, storePath, "e2e-second", []cmRule{ruleB}, nil)

	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "kill", "-f", sessionA)
	testutil.AssertSessionNotExists(t, logger, sessionA)

	sessionB := fmt.Sprintf("e2e_cm_mem_b_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = execCommand("ntm", "--config", configPath, "kill", "-f", sessionB)
	})

	logger.LogSection("Spawn session B with updated memory")
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "spawn", sessionB, "--cc=1", "--json", "--safety", "--no-hooks")
	waitForPaneContains(t, logger, sessionB, 1, ruleB.Content, 6*time.Second)
}

func TestE2ECMMultiAgentContextInjection(t *testing.T) {
	testutil.E2ETestPrecheck(t)
	testutil.RequireUnix(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	binDir := t.TempDir()
	writeCMBinary(t, binDir)

	storePath := filepath.Join(t.TempDir(), "cm_store.json")
	logPath := filepath.Join(t.TempDir(), "cm_calls.log")

	rule := cmRule{ID: "team-1", Content: "Keep changes focused and small", Category: "discipline"}
	writeCMStore(t, storePath, "e2e-multi", []cmRule{rule}, nil)

	pathEnv := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", pathEnv)
	t.Setenv("CM_E2E_STORE", storePath)
	t.Setenv("CM_E2E_LOG", logPath)

	projectsBase := t.TempDir()
	configPath := writeTestConfig(t, t.TempDir(), projectsBase)

	session := fmt.Sprintf("e2e_cm_multi_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = execCommand("ntm", "--config", configPath, "kill", "-f", session)
	})

	logger.LogSection("Spawn session with 3 agents")
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "spawn", session, "--cc=3", "--json", "--safety", "--no-hooks")

	// Verify each agent pane received the memory context.
	for pane := 1; pane <= 3; pane++ {
		waitForPaneContains(t, logger, session, pane, rule.Content, 6*time.Second)
	}
}

func execCommand(cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	return c.Run()
}
