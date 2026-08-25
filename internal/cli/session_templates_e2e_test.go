package cli

import (
	"bytes"
	"encoding/json"
	"errors"
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true

	configureSessionTemplateFakeAgents(cfg)

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

	if err := spawnSessionLogicContext(t.Context(), opts); err != nil {
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
	// Check for substring that won't be wrapped by batcat/terminal formatting.
	// The full prompt contains "You are part of a code review team" but terminal
	// wrapping can split "team" onto the next line, so check for a shorter phrase.
	if tmpl.Spec.Prompts.Initial != "" && !strings.Contains(output, "code review") {
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true

	configureSessionTemplateFakeAgents(cfg)

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

	if err := spawnSessionLogicContext(t.Context(), opts); err != nil {
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
	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runSessionTemplatesShow error = %v, want errJSONFailure", err)
	}

	var resp struct {
		Success     bool     `json:"success"`
		Error       string   `json:"error"`
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}
	if resp.Success {
		t.Fatal("expected success=false for invalid template")
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "validation failed") {
		t.Fatalf("expected validation error, got %q", resp.Error)
	}
	if len(resp.Suggestions) == 0 {
		t.Fatalf("expected suggestions in error response")
	}
}

func TestSessionTemplatesList_InvalidTemplateReturnsTerminalJSONFailure(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	templateDir := filepath.Join(configHome, "ntm", "templates")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatalf("create template directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "invalid.yaml"), []byte("metadata: [\n"), 0644); err != nil {
		t.Fatalf("write invalid template: %v", err)
	}

	output, runErr := captureStdout(t, runSessionTemplatesList)
	if !errors.Is(runErr, errJSONFailure) {
		t.Fatalf("runSessionTemplatesList error = %v, want errJSONFailure", runErr)
	}
	document := decodeSingleTerminalJSONMap(t, output)
	if success, ok := document["success"].(bool); !ok || success {
		t.Fatalf("success = %#v, want false", document["success"])
	}
	errorMessage, ok := document["error"].(string)
	if !ok || errorMessage == "" {
		t.Fatalf("error = %#v, want non-empty loader error", document["error"])
	}
}

func TestSessionTemplatesShow_MissingTemplateReturnsTerminalJSONFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	output, runErr := captureStdout(t, func() error {
		return runSessionTemplatesShow("definitely-missing-template")
	})
	if !errors.Is(runErr, errJSONFailure) {
		t.Fatalf("runSessionTemplatesShow error = %v, want errJSONFailure", runErr)
	}
	if !strings.Contains(runErr.Error(), "session template not found") {
		t.Fatalf("runSessionTemplatesShow error = %v, want preserved not-found cause", runErr)
	}

	document := decodeSingleTerminalJSONMap(t, output)
	if success, ok := document["success"].(bool); !ok || success {
		t.Fatalf("success = %#v, want false", document["success"])
	}
	errorMessage, ok := document["error"].(string)
	if !ok || !strings.Contains(errorMessage, "session template not found") {
		t.Fatalf("error = %#v, want session template not found", document["error"])
	}
	if suggestions, ok := document["suggestions"].([]interface{}); !ok || len(suggestions) == 0 {
		t.Fatalf("suggestions = %#v, want non-empty array", document["suggestions"])
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

	// Read in a background goroutine to prevent deadlock when f writes
	// more than the pipe buffer (64 KB on Linux).
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	runErr := f()
	_ = w.Close()
	os.Stdout = old

	<-done // wait for reader to drain
	_ = r.Close()
	return buf.String(), runErr
}

func configureSessionTemplateFakeAgents(testCfg *config.Config) {
	const modelPrefix = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}`
	testCfg.Agents.Claude = modelPrefix + `/bin/sh -c 'stty -echo; printf "Claude Code v0.0.0\n\342\235\257 \n"; while IFS= read -r line; do printf "RECEIVED:%s\n\342\235\257 \n" "$line"; done'`
	testCfg.Agents.Codex = modelPrefix + `/bin/sh -c 'stty -echo; printf "Codex CLI\ncodex>\n"; while IFS= read -r line; do printf "RECEIVED:%s\ncodex>\n" "$line"; done'`
	testCfg.Agents.Gemini = modelPrefix + `/bin/sh -c 'stty -echo; printf "Gemini CLI\ngemini>\n"; while IFS= read -r line; do printf "RECEIVED:%s\ngemini>\n" "$line"; done'`
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
