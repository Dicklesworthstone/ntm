package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestStatusRealSession tests status command output with a real tmux session
func TestStatusRealSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-status")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save/Restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = false // Test text output

	// Use simple command
	cfg.Agents.Claude = testAgentCatCommandTemplate // Runs until killed or input closed

	sessionName := fmt.Sprintf("ntm-test-status-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Define agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "claude-test"},
	}

	// Create project dir
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn session
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}
	err = spawnSessionLogicContext(t.Context(), opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Wait for session to settle
	time.Sleep(500 * time.Millisecond)

	// Run status and capture output
	var buf bytes.Buffer
	err = runStatus(t.Context(), &buf, sessionName, statusOptions{})
	if err != nil {
		t.Fatalf("runStatus failed: %v", err)
	}

	output := stripANSI(buf.String())

	// Verify output contains key info
	// Note: Full pane titles are truncated in the table display, so we verify the Claude
	// pane exists via the agent type indicator (C) and the Agents summary
	checks := []string{
		sessionName,
		"Panes",
		"Directory:",
		"Claude",
		"1 instance(s)",
		"C ", // Claude pane type indicator in the pane list
	}

	for _, check := range checks {
		if !regexp.MustCompile(regexp.QuoteMeta(check)).MatchString(output) {
			t.Errorf("output missing %q\nGot:\n%s", check, output)
		}
	}
}

// TestStatusPaneNumbersAreSendSelectors is the regression test for ntm-bb87:
// the number printed next to each pane in `ntm status` must be accepted by
// `ntm send --pane=` and address that exact pane. Historically status rendered
// a 1-based enumeration ordinal while send resolved the 0-based tmux pane
// index, silently misrouting every per-pane dispatch.
func TestStatusPaneNumbersAreSendSelectors(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir, err := os.MkdirTemp("", "ntm-test-status-selectors")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = false
	cfg.Agents.Claude = testAgentCatCommandTemplate

	sessionName := fmt.Sprintf("ntm-test-selectors-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}
	opts := SpawnOptions{
		Session: sessionName,
		Agents: []FlatAgent{
			{Type: AgentTypeClaude, Index: 1, Model: "claude-test"},
			{Type: AgentTypeClaude, Index: 2, Model: "claude-test"},
		},
		CCCount:  2,
		UserPane: true,
	}
	if err := spawnSessionLogicContext(t.Context(), opts); err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	var buf bytes.Buffer
	if err := runStatus(t.Context(), &buf, sessionName, statusOptions{}); err != nil {
		t.Fatalf("runStatus failed: %v", err)
	}
	output := stripANSI(buf.String())

	// Extract the leading selector of each rendered pane row (lines between
	// the Panes header separator and the selector legend).
	lines := strings.Split(output, "\n")
	inPanes := false
	var selectors []string
	rowPattern := regexp.MustCompile(`^\s+(\d+(?:\.\d+)?)\s`)
	for _, line := range lines {
		if strings.Contains(line, "Panes") && !strings.Contains(line, ":") {
			inPanes = true
			continue
		}
		if strings.Contains(line, "Pane numbers are tmux selectors") {
			break
		}
		if !inPanes {
			continue
		}
		if m := rowPattern.FindStringSubmatch(line); m != nil {
			selectors = append(selectors, m[1])
		}
	}
	if len(selectors) < 3 {
		t.Fatalf("expected at least 3 pane selectors in status output, got %v\nOutput:\n%s", selectors, output)
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}
	// Every displayed number must resolve to exactly one pane via the same
	// resolver `ntm send --pane=` uses.
	for _, sel := range selectors {
		resolved, err := tmux.ResolvePaneSelectors(panes, []string{sel}, true)
		if err != nil {
			t.Errorf("displayed selector %q rejected by send resolver: %v", sel, err)
			continue
		}
		if len(resolved) != 1 {
			t.Errorf("displayed selector %q resolved to %d panes, want 1", sel, len(resolved))
		}
	}
	// The displayed selectors must cover every pane exactly once.
	if len(selectors) != len(panes) {
		t.Errorf("status displayed %d selectors for %d panes", len(selectors), len(panes))
	}

	// A nonexistent displayed index must error and name the valid selectors.
	_, err = tmux.ResolvePaneSelectors(panes, []string{"99"}, true)
	if err == nil {
		t.Fatal("expected error resolving nonexistent selector 99")
	}
	if !strings.Contains(err.Error(), "available") {
		t.Errorf("nonexistent-selector error should list available panes, got: %v", err)
	}
}

func TestFetchAgentMailStatusSkipsEmptyProjectKey(t *testing.T) {
	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)

	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })

	status := fetchAgentMailStatus("")
	if status == nil {
		t.Fatal("fetchAgentMailStatus returned nil")
	}
	if !status.Available {
		t.Fatalf("expected available status when server health endpoint is reachable")
	}
	if status.Connected {
		t.Fatalf("expected disconnected status for empty project key")
	}
	if stub.ensureCalled != 0 {
		t.Fatalf("expected no ensure_project call for empty project key, got %d", stub.ensureCalled)
	}
}
