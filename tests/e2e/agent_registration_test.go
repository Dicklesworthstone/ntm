package e2e

import (
	"context"
	cryptorand "crypto/rand"
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

func agentRegistrationSessionName(t *testing.T, prefix string) string {
	t.Helper()
	var suffix [8]byte
	if _, err := cryptorand.Read(suffix[:]); err != nil {
		t.Fatalf("generate session suffix: %v", err)
	}
	return fmt.Sprintf("%s_%x", prefix, suffix)
}

// TestE2EAgentMailAutoRegistration tests that agents spawned with ntm
// are automatically registered with Agent Mail and that pane-to-agent
// name mappings are persisted for session recovery.
func TestE2EAgentMailAutoRegistration(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)
	client := requireAgentMail(t)

	session := agentRegistrationSessionName(t, "am_autoreg")
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

	// #240: panes added to a live session must be registered with Agent Mail
	// too. Before the fix, `ntm add` launched the agent but never called the
	// registration helper, so the registry stayed at its spawn-time size and
	// the added pane had no identity to send or receive mail with.
	addOut := runCmd(t, projectDir, "ntm", "--config", configPath, "--json", "add", session, "--cc=1")
	t.Logf("Add output: %s", string(addOut))
	addJSON := string(addOut)
	if start := strings.Index(addJSON, "{"); start > 0 {
		addJSON = addJSON[start:]
	}
	var addResp struct {
		TotalAdded int `json:"total_added"`
		NewPanes   []struct {
			PaneID string `json:"pane_id"`
			Title  string `json:"title"`
		} `json:"new_panes"`
		AgentMail *struct {
			Available        bool              `json:"available"`
			AgentsRegistered int               `json:"agents_registered"`
			AgentsFailed     int               `json:"agents_failed"`
			AgentMap         map[string]string `json:"agent_map"`
		} `json:"agent_mail"`
	}
	if err := json.Unmarshal([]byte(addJSON), &addResp); err != nil {
		t.Fatalf("decode add response: %v raw=%s", err, addOut)
	}
	if addResp.TotalAdded != 1 || len(addResp.NewPanes) != 1 {
		t.Fatalf("add response=%+v, want exactly one added pane", addResp)
	}
	addedPane := addResp.NewPanes[0]
	if addResp.AgentMail == nil {
		t.Fatalf("add response omitted agent_mail status: %s", addOut)
	}
	if !addResp.AgentMail.Available || addResp.AgentMail.AgentsRegistered != 1 || addResp.AgentMail.AgentsFailed != 0 {
		t.Fatalf("add agent_mail status=%+v, want the added pane registered", addResp.AgentMail)
	}
	if name := addResp.AgentMail.AgentMap[addedPane.PaneID]; name == "" {
		t.Fatalf("add agent_mail map=%v, want an identity for pane %s", addResp.AgentMail.AgentMap, addedPane.PaneID)
	}

	addedRegistry, err := agentmail.LoadSessionAgentRegistry(session, projectDir)
	if err != nil {
		t.Fatalf("load registry after add: %v", err)
	}
	if addedRegistry == nil || addedRegistry.Count() != 4 {
		t.Fatalf("registry after add=%+v, want the 3 spawned agents plus the added one", addedRegistry)
	}
	if name, ok := addedRegistry.GetAgentByTitle(addedPane.Title); !ok || name == "" {
		t.Errorf("added pane title %q not registered (name=%q)", addedPane.Title, name)
	}
}

// TestE2EAgentMailAdoptRegistration proves that adoption follows the same
// identity contract as spawn and add. The session is created directly through
// tmux so it has no pre-existing NTM or Agent Mail state.
func TestE2EAgentMailAdoptRegistration(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)
	client := requireAgentMail(t)

	session := agentRegistrationSessionName(t, "am_adopt")
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configPath := writeAgentMailTestConfig(t, projectsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := client.EnsureProject(ctx, projectDir)
	cancel()
	if err != nil {
		t.Fatalf("ensure Agent Mail project: %v", err)
	}
	if err := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session, "-c", projectDir, "sleep 30").Run(); err != nil {
		t.Fatalf("create external tmux session: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	runCmd(t, projectDir, "ntm", "--config", configPath, "--json", "adopt", session, "--cc=0")

	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("list adopted panes: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("adopted panes = %d, want 1", len(panes))
	}
	if !strings.Contains(panes[0].Title, "__cc_1") {
		t.Fatalf("adopted pane title = %q, want canonical Claude title", panes[0].Title)
	}

	registry, err := agentmail.LoadSessionAgentRegistry(session, projectDir)
	if err != nil {
		t.Fatalf("load adopted session registry: %v", err)
	}
	if registry == nil || registry.Count() != 1 {
		t.Fatalf("adopted registry = %+v, want one registered pane", registry)
	}
	if name, ok := registry.GetAgent(panes[0].Title, panes[0].ID); !ok || name == "" {
		t.Fatalf("adopted pane %q (%s) has no registered identity: %q", panes[0].Title, panes[0].ID, name)
	}
	if name, path := agentmail.ResolveIdentity(projectDir, panes[0].ID); name == "" || path == "" {
		t.Fatalf("resolve adopted pane identity = %q at %q; want persisted identity", name, path)
	}
}

// TestE2EAgentMailRegistryRecovery tests that persisted agent mappings
// can be loaded after session restart for routing recovery.
func TestE2EAgentMailRegistryRecovery(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)
	_ = requireAgentMail(t)

	session := agentRegistrationSessionName(t, "am_recovery")
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
