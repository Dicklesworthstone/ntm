package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/templates"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestSessionTemplateSpawn_Builtin(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir := t.TempDir()

	oldCfg := cfg
	oldJSON := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	cfg.AgentMail.Enabled = false
	jsonOutput = true

	cfg.Agents.Claude = "cat"
	cfg.Agents.Codex = "cat"
	cfg.Agents.Gemini = "cat"

	t.Logf("[E2E-TEMPLATE] Loading builtin template: code-review")
	loader := templates.NewSessionTemplateLoader()
	tmpl, err := loader.Load("code-review")
	if err != nil {
		t.Fatalf("Load(code-review) failed: %v", err)
	}

	specs, counts := agentSpecsFromSessionTemplate(tmpl.Spec.Agents)
	agents := specs.Flatten()
	if len(agents) == 0 {
		t.Fatalf("template produced no agents")
	}

	sessionName := fmt.Sprintf("ntm-template-e2e-%d", time.Now().UnixNano())
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll(projectDir) failed: %v", err)
	}
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	t.Logf("[E2E-TEMPLATE] Spawning session %s with template agents (cc=%d cod=%d gmi=%d)", sessionName, counts.cc, counts.cod, counts.gmi)
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  counts.cc,
		CodCount: counts.cod,
		GmiCount: counts.gmi,
		UserPane: true,
		Prompt:   tmpl.Spec.Prompts.Initial,
	}

	if err := spawnSessionLogic(opts); err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	time.Sleep(400 * time.Millisecond)

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	expectedPanes := len(agents) + 1
	if len(panes) != expectedPanes {
		t.Fatalf("expected %d panes (agents + user), got %d", expectedPanes, len(panes))
	}

	// Verify we created at least the expected agent types
	var foundClaude, foundCodex bool
	for _, pane := range panes {
		switch pane.Type {
		case tmux.AgentClaude:
			foundClaude = true
		case tmux.AgentCodex:
			foundCodex = true
		}
	}
	if counts.cc > 0 && !foundClaude {
		t.Fatalf("expected Claude pane(s), found none")
	}
	if counts.cod > 0 && !foundCodex {
		t.Fatalf("expected Codex pane(s), found none")
	}

	// Verify prompt delivered (check any agent pane)
	var agentPaneID string
	for _, pane := range panes {
		if pane.Type != tmux.AgentUser {
			agentPaneID = pane.ID
			break
		}
	}
	if agentPaneID == "" {
		t.Fatalf("no agent pane found for prompt verification")
	}

	time.Sleep(400 * time.Millisecond)
	output, err := tmux.CapturePaneOutput(agentPaneID, 50)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if tmpl.Spec.Prompts.Initial != "" && !strings.Contains(output, "code review team") {
		t.Errorf("expected initial prompt to be delivered; output:\n%s", output)
	}
}

func TestSessionTemplateSpawn_CustomUserTemplate(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	oldCfg := cfg
	oldJSON := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	cfg.AgentMail.Enabled = false
	jsonOutput = true

	cfg.Agents.Claude = "cat"
	cfg.Agents.Codex = "cat"
	cfg.Agents.Gemini = "cat"

	userTemplateDir := filepath.Join(tmpDir, "ntm", "templates")
	if err := os.MkdirAll(userTemplateDir, 0755); err != nil {
		t.Fatalf("MkdirAll(userTemplateDir) failed: %v", err)
	}

	templateName := "custom-e2e"
	templateContent := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: custom-e2e
  description: Custom template for e2e
spec:
  agents:
    claude:
      count: 1
  prompts:
    initial: "Custom template prompt for e2e test"
`
	if err := os.WriteFile(filepath.Join(userTemplateDir, templateName+".yaml"), []byte(templateContent), 0644); err != nil {
		t.Fatalf("WriteFile(template) failed: %v", err)
	}

	t.Logf("[E2E-TEMPLATE] Loading custom user template: %s", templateName)
	loader := templates.NewSessionTemplateLoaderWithProject(tmpDir)
	tmpl, err := loader.Load(templateName)
	if err != nil {
		t.Fatalf("Load(custom-e2e) failed: %v", err)
	}

	specs, counts := agentSpecsFromSessionTemplate(tmpl.Spec.Agents)
	agents := specs.Flatten()
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}

	sessionName := fmt.Sprintf("ntm-template-custom-%d", time.Now().UnixNano())
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll(projectDir) failed: %v", err)
	}
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	t.Logf("[E2E-TEMPLATE] Spawning session %s with custom template", sessionName)
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  counts.cc,
		CodCount: counts.cod,
		GmiCount: counts.gmi,
		UserPane: true,
		Prompt:   tmpl.Spec.Prompts.Initial,
	}

	if err := spawnSessionLogic(opts); err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	time.Sleep(400 * time.Millisecond)
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}
	if len(panes) != 2 { // user + 1 agent
		t.Fatalf("expected 2 panes, got %d", len(panes))
	}

	var agentPaneID string
	for _, pane := range panes {
		if pane.Type != tmux.AgentUser {
			agentPaneID = pane.ID
			break
		}
	}
	if agentPaneID == "" {
		t.Fatalf("no agent pane found for prompt verification")
	}
	time.Sleep(400 * time.Millisecond)
	output, err := tmux.CapturePaneOutput(agentPaneID, 50)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if !strings.Contains(output, "Custom template prompt") {
		t.Errorf("expected custom prompt in output, got:\n%s", output)
	}
}

func TestSessionTemplatesList_IncludesBuiltinAndUser(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = true

	userTemplateDir := filepath.Join(tmpDir, "ntm", "templates")
	if err := os.MkdirAll(userTemplateDir, 0755); err != nil {
		t.Fatalf("MkdirAll(userTemplateDir) failed: %v", err)
	}

	templateName := "custom-list"
	templateContent := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: custom-list
  description: Custom list template for e2e
spec:
  agents:
    claude:
      count: 1
`
	if err := os.WriteFile(filepath.Join(userTemplateDir, templateName+".yaml"), []byte(templateContent), 0644); err != nil {
		t.Fatalf("WriteFile(template) failed: %v", err)
	}

	t.Logf("[E2E-TEMPLATE] Listing templates (builtin + user)")
	output, err := captureStdout(t, runSessionTemplatesList)
	if err != nil {
		t.Fatalf("runSessionTemplatesList failed: %v", err)
	}

	var result SessionTemplatesListResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	var foundBuiltin, foundUser bool
	for _, tmpl := range result.Templates {
		switch {
		case tmpl.Name == "code-review" && tmpl.Source == "builtin":
			foundBuiltin = true
		case tmpl.Name == templateName && tmpl.Source == "user":
			foundUser = true
			if tmpl.Description != "Custom list template for e2e" {
				t.Fatalf("expected description to match, got %q", tmpl.Description)
			}
		}
	}

	if !foundBuiltin {
		t.Fatalf("expected builtin template code-review in list")
	}
	if !foundUser {
		t.Fatalf("expected user template %s in list", templateName)
	}
}

func TestSessionTemplatesShow_InvalidTemplateIncludesSuggestions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = true

	userTemplateDir := filepath.Join(tmpDir, "ntm", "templates")
	if err := os.MkdirAll(userTemplateDir, 0755); err != nil {
		t.Fatalf("MkdirAll(userTemplateDir) failed: %v", err)
	}

	templateName := "bad-template"
	templateContent := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: "bad name"
spec:
  agents:
    claude:
      count: 1
`
	if err := os.WriteFile(filepath.Join(userTemplateDir, templateName+".yaml"), []byte(templateContent), 0644); err != nil {
		t.Fatalf("WriteFile(template) failed: %v", err)
	}

	t.Logf("[E2E-TEMPLATE] Showing invalid template: %s", templateName)
	output, err := captureStdout(t, func() error { return runSessionTemplatesShow(templateName) })
	if err != nil {
		t.Fatalf("runSessionTemplatesShow failed: %v", err)
	}

	var resp struct {
		Error       string   `json:"error"`
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "validation failed") {
		t.Fatalf("expected validation error, got %q", resp.Error)
	}
	if len(resp.Suggestions) == 0 {
		t.Fatalf("expected suggestions in error response")
	}
}

func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stdout = w

	runErr := f()
	_ = w.Close()
	os.Stdout = old

	output, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatalf("ReadAll failed: %v", readErr)
	}
	return string(output), runErr
}

type templateCounts struct {
	cc  int
	cod int
	gmi int
}

func agentSpecsFromSessionTemplate(spec templates.AgentsSpec) (AgentSpecs, templateCounts) {
	var specs AgentSpecs
	counts := templateCounts{}

	if spec.Claude != nil {
		if len(spec.Claude.Variants) > 0 {
			for _, v := range spec.Claude.Variants {
				specs = append(specs, AgentSpec{Type: AgentTypeClaude, Count: v.Count, Model: v.Model})
				counts.cc += v.Count
			}
		} else if spec.Claude.Count > 0 {
			specs = append(specs, AgentSpec{Type: AgentTypeClaude, Count: spec.Claude.Count, Model: spec.Claude.Model})
			counts.cc += spec.Claude.Count
		}
	}

	if spec.Codex != nil {
		if len(spec.Codex.Variants) > 0 {
			for _, v := range spec.Codex.Variants {
				specs = append(specs, AgentSpec{Type: AgentTypeCodex, Count: v.Count, Model: v.Model})
				counts.cod += v.Count
			}
		} else if spec.Codex.Count > 0 {
			specs = append(specs, AgentSpec{Type: AgentTypeCodex, Count: spec.Codex.Count, Model: spec.Codex.Model})
			counts.cod += spec.Codex.Count
		}
	}

	if spec.Gemini != nil {
		if len(spec.Gemini.Variants) > 0 {
			for _, v := range spec.Gemini.Variants {
				specs = append(specs, AgentSpec{Type: AgentTypeGemini, Count: v.Count, Model: v.Model})
				counts.gmi += v.Count
			}
		} else if spec.Gemini.Count > 0 {
			specs = append(specs, AgentSpec{Type: AgentTypeGemini, Count: spec.Gemini.Count, Model: spec.Gemini.Model})
			counts.gmi += spec.Gemini.Count
		}
	}

	return specs, counts
}
