package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestE2EAgentMailAutoRegistration tests that agents spawned with ntm
// are automatically registered with Agent Mail and that pane-to-agent
// name mappings are persisted for session recovery.
func TestE2EAgentMailAutoRegistration(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)
	client := requireAgentMail(t)

	session := fmt.Sprintf("am_autoreg_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}

	// Override XDG_CONFIG_HOME to isolate session data
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	configPath := writeAgentMailTestConfig(t, projectsBase)

	// Ensure Agent Mail project exists
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := client.EnsureProject(ctx, projectDir)
	cancel()
	if err != nil {
		t.Fatalf("ensure Agent Mail project: %v", err)
	}

	t.Cleanup(func() {
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	// Spawn 2 Claude + 1 Codex agents
	spawnOut := runCmd(t, projectDir, "ntm", "--config", configPath, "--json", "spawn", session, "--cc=2", "--cod=1")
	t.Logf("Spawn output: %s", string(spawnOut))

	// Wait for agents to be ready
	time.Sleep(1 * time.Second)

	// Verify pane count
	listOut, err := exec.Command(tmux.BinaryPath(), "list-panes", "-t", session, "-F", "#{pane_title}").Output()
	if err != nil {
		t.Fatalf("list panes: %v", err)
	}
	paneTitles := strings.Split(strings.TrimSpace(string(listOut)), "\n")
	agentPanes := filterAgentPanes(paneTitles)
	if len(agentPanes) != 3 {
		t.Fatalf("expected 3 agent panes, got %d: %v", len(agentPanes), paneTitles)
	}
	t.Logf("Agent panes: %v", agentPanes)

	// Verify agents were registered with Agent Mail
	testutil.AssertEventually(t, testutil.NewTestLoggerStdout(t), 10*time.Second, 500*time.Millisecond,
		"agents registered with Agent Mail", func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			agents, err := client.ListProjectAgents(ctx, projectDir)
			if err != nil {
				t.Logf("list agents error: %v", err)
				return false
			}
			// Expect at least 3 agents (could be more if session agent also registered)
			return len(agents) >= 3
		})

	// Verify registry file was persisted
	projectSlug := filepath.Base(projectDir)
	registryPath := filepath.Join(configHome, "ntm", "sessions", session, projectSlug, "agent_registry.json")

	testutil.AssertEventually(t, testutil.NewTestLoggerStdout(t), 5*time.Second, 250*time.Millisecond,
		"registry file created", func() bool {
			_, err := os.Stat(registryPath)
			return err == nil
		})

	// Load and verify registry content
	registry, err := agentmail.LoadSessionAgentRegistry(session, projectDir)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if registry == nil {
		t.Fatal("registry is nil")
	}

	// Verify agent count in registry
	if registry.Count() != 3 {
		t.Errorf("expected 3 agents in registry, got %d", registry.Count())
		data, _ := json.MarshalIndent(registry, "", "  ")
		t.Logf("Registry content: %s", string(data))
	}

	// Verify each pane title has a mapping
	for _, paneTitle := range agentPanes {
		agentName, ok := registry.GetAgentByTitle(paneTitle)
		if !ok {
			t.Errorf("pane title %q not found in registry", paneTitle)
			continue
		}
		if agentName == "" {
			t.Errorf("pane title %q has empty agent name", paneTitle)
		}
		t.Logf("Mapping: %s -> %s", paneTitle, agentName)
	}

	// Verify pane ID mappings are also populated
	if len(registry.PaneIDMap) != 3 {
		t.Errorf("expected 3 pane ID mappings, got %d", len(registry.PaneIDMap))
	}

	// Verify project key matches
	if registry.ProjectKey != projectDir {
		t.Errorf("project key mismatch: got %q, want %q", registry.ProjectKey, projectDir)
	}
}

// TestE2EAgentMailRegistryRecovery tests that persisted agent mappings
// can be loaded after session restart for routing recovery.
func TestE2EAgentMailRegistryRecovery(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)
	_ = requireAgentMail(t)

	session := fmt.Sprintf("am_recovery_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}

	// Override XDG_CONFIG_HOME to isolate session data
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	configPath := writeAgentMailTestConfig(t, projectsBase)

	t.Cleanup(func() {
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	// Spawn agents
	runCmd(t, projectDir, "ntm", "--config", configPath, "spawn", session, "--cc=1")
	time.Sleep(1 * time.Second)

	// Verify registry was created
	registry1, err := agentmail.LoadSessionAgentRegistry(session, projectDir)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if registry1 == nil || registry1.Count() == 0 {
		t.Skip("registry not populated - Agent Mail may not be functioning")
	}

	// Capture original mappings
	originalMappings := make(map[string]string)
	for title, name := range registry1.Agents {
		originalMappings[title] = name
	}
	t.Logf("Original mappings: %v", originalMappings)

	// Kill the session
	_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	time.Sleep(500 * time.Millisecond)

	// Verify registry persists after session death
	registry2, err := agentmail.LoadSessionAgentRegistry(session, projectDir)
	if err != nil {
		t.Fatalf("load registry after kill: %v", err)
	}
	if registry2 == nil {
		t.Fatal("registry was deleted after session kill")
	}

	// Verify mappings are preserved
	for title, expectedName := range originalMappings {
		actualName, ok := registry2.GetAgentByTitle(title)
		if !ok {
			t.Errorf("mapping lost for %q", title)
			continue
		}
		if actualName != expectedName {
			t.Errorf("mapping changed for %q: got %q, want %q", title, actualName, expectedName)
		}
	}
}

// filterAgentPanes returns only panes that match agent naming patterns.
func filterAgentPanes(paneTitles []string) []string {
	var result []string
	for _, title := range paneTitles {
		if strings.Contains(title, "__cc_") || strings.Contains(title, "__cod_") || strings.Contains(title, "__gmi_") {
			result = append(result, title)
		}
	}
	return result
}
