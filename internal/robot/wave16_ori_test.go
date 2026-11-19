package robot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestWave16ValidationEngineAnd11HarnessExecutables(t *testing.T) {
	tmpDir := t.TempDir()
	userCfgDir := filepath.Join(tmpDir, "user-ntm")
	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir userCfgDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}

	mainConfigPath := filepath.Join(userCfgDir, "config.toml")
	missingProjectsBase := filepath.Join(tmpDir, "auto-created-projects")
	mainTOML := `projects_base = ` + `"` + missingProjectsBase + `"` + "\n"
	if err := os.WriteFile(mainConfigPath, []byte(mainTOML), 0o644); err != nil {
		t.Fatalf("write main config: %v", err)
	}

	policyPath := filepath.Join(projectDir, ".ntm", "policy.yaml")
	policyYAML := "version: 1\nblocked:\n  - pattern: \"git reset --hard\"\n    reason: \"forbidden\"\n"
	if err := os.WriteFile(policyPath, []byte(policyYAML), 0o644); err != nil {
		t.Fatalf("write policy.yaml: %v", err)
	}

	t.Setenv("NTM_CONFIG", mainConfigPath)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))

	out, err := GetValidation(ValidationOptions{
		MainConfigPath: mainConfigPath,
		ProjectDir:     projectDir,
		All:            true,
		Fix:            true,
	})
	if err != nil {
		t.Fatalf("GetValidation: %v", err)
	}
	if !out.Success || !out.Valid {
		t.Fatalf("expected valid config report, got %+v", out)
	}
	if !DirExists(missingProjectsBase) {
		t.Fatalf("expected --validate-fix to create %s", missingProjectsBase)
	}

	// Negative check: invalid TOML in recipes.toml triggers VALIDATION_FAILED
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "recipes.toml"), []byte("[broken\n"), 0o644); err != nil {
		t.Fatalf("write broken recipes.toml: %v", err)
	}
	badOut, err := GetValidation(ValidationOptions{
		MainConfigPath: mainConfigPath,
		ProjectDir:     projectDir,
	})
	if err != nil {
		t.Fatalf("GetValidation(broken): %v", err)
	}
	if badOut.Success || badOut.Valid || badOut.ErrorCode != "VALIDATION_FAILED" || badOut.Summary.ErrorCount == 0 {
		t.Fatalf("expected VALIDATION_FAILED, got %+v", badOut)
	}

	// Verify 11-harness executable checks cover newly wired harnesses (cursor, windsurf, aider, antigravity, oc, ollama)
	cfg := config.Default()
	cfg.Agents.Cursor = "/nonexistent/wave16-cursor"
	cfg.Agents.Windsurf = "/nonexistent/wave16-windsurf"
	cfg.Agents.Aider = "/nonexistent/wave16-aider"
	cfg.Agents.Antigravity = "/nonexistent/wave16-agy"
	cfg.Agents.Opencode = "/nonexistent/wave16-oc"
	cfg.Agents.Ollama = "/nonexistent/wave16-ollama"

	execRes := &ValidationResult{Valid: true}
	ValidateAgentExecutables(cfg, execRes)
	warnFields := make(map[string]bool)
	for _, w := range execRes.Warnings {
		warnFields[w.Field] = true
	}
	for _, wantField := range []string{
		"agents.cursor",
		"agents.windsurf",
		"agents.aider",
		"agents.antigravity",
		"agents.oc",
		"agents.ollama",
	} {
		if !warnFields[wantField] {
			t.Fatalf("expected warning for %s, got warnings=%+v", wantField, execRes.Warnings)
		}
	}
}

func TestWave16PluginsAndLocalModelsEngines(t *testing.T) {
	cfgDir := t.TempDir()
	agentsDir := filepath.Join(cfgDir, "agents")
	cmdsDir := filepath.Join(cfgDir, "commands")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	if err := os.MkdirAll(cmdsDir, 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}

	agentTOML := `
[agent]
name = "hermes"
alias = "hm"
command = "hermes-cli --chat"
description = "Hermes custom coding harness"

[agent.env]
HERMES_API_KEY = "super-secret-value-do-not-emit"
HERMES_MODE = "fast"

[agent.defaults]
model = "hermes-4"
tags = ["custom", "fast"]

[agent.readiness]
idle_patterns = ["hermes> "]
`
	if err := os.WriteFile(filepath.Join(agentsDir, "hermes.toml"), []byte(agentTOML), 0o644); err != nil {
		t.Fatalf("write agent plugin: %v", err)
	}

	cmdScript := "#!/bin/sh\n# Description: Run custom lint suite\n# Usage: ntm custom-lint [pkg]\necho ok\n"
	if err := os.WriteFile(filepath.Join(cmdsDir, "custom-lint"), []byte(cmdScript), 0o755); err != nil {
		t.Fatalf("write command plugin: %v", err)
	}

	plugOut, err := GetPlugins(PluginsOptions{ConfigDir: cfgDir})
	if err != nil {
		t.Fatalf("GetPlugins: %v", err)
	}
	if !plugOut.Success || plugOut.TotalCount != 2 || len(plugOut.AgentPlugins) != 1 || len(plugOut.CommandPlugins) != 1 {
		t.Fatalf("unexpected GetPlugins output: %+v", plugOut)
	}
	ap := plugOut.AgentPlugins[0]
	if ap.Name != "hermes" || ap.Alias != "hm" || ap.ProbeCommand != "hermes-cli" || !ap.HasReadiness {
		t.Fatalf("unexpected agent plugin metadata: %+v", ap)
	}
	rawJSON, err := json.Marshal(plugOut)
	if err != nil {
		t.Fatalf("marshal plugins output: %v", err)
	}
	if strings.Contains(string(rawJSON), "super-secret-value-do-not-emit") {
		t.Fatalf("GetPlugins leaked secret env value in JSON: %s", string(rawJSON))
	}
	if len(ap.EnvKeys) != 2 || ap.EnvKeys[0] != "HERMES_API_KEY" || ap.EnvKeys[1] != "HERMES_MODE" {
		t.Fatalf("expected sorted env_keys without values, got %+v", ap.EnvKeys)
	}

	// Local models recommend + list against real HTTP test server + SSRF host check
	t.Setenv("NTM_LOCAL_VRAM_GB", "12")
	recOut, err := GetLocalModels(context.Background(), LocalModelsOptions{Action: "recommend"})
	if err != nil {
		t.Fatalf("GetLocalModels(recommend): %v", err)
	}
	if !recOut.Success || recOut.DetectedVRAMGB != 12 || recOut.VRAMSource != "env:NTM_LOCAL_VRAM_GB" || len(recOut.Recommendations) == 0 {
		t.Fatalf("unexpected recommend output: %+v", recOut)
	}

	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{
					"name":        "deepseek-coder:33b",
					"size":        int64(18 * 1024 * 1024 * 1024),
					"digest":      "sha256:111",
					"modified_at": time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
					"details":     map[string]any{"family": "deepseek", "parameter_size": "33B"},
				},
			},
		})
	}))
	defer ollamaSrv.Close()

	listOut, err := GetLocalModels(context.Background(), LocalModelsOptions{
		Action: "list",
		Host:   ollamaSrv.URL,
	})
	if err != nil {
		t.Fatalf("GetLocalModels(list): %v", err)
	}
	if !listOut.Success || listOut.ModelCount != 1 || listOut.Models[0].Name != "deepseek-coder:33b" {
		t.Fatalf("unexpected list output: %+v", listOut)
	}

	badAction, err := GetLocalModels(context.Background(), LocalModelsOptions{Action: "purge"})
	if err != nil || badAction.Success || badAction.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for bad models action, got %+v (%v)", badAction, err)
	}

	// SSRF guard checks
	if err := ValidateSafeOllamaHostOverride("http://127.0.0.1:11434"); err != nil {
		t.Fatalf("expected loopback IPv4 allowed, got %v", err)
	}
	if err := ValidateSafeOllamaHostOverride("http://localhost:11434"); err != nil {
		t.Fatalf("expected localhost allowed, got %v", err)
	}
	for _, forbidden := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.25:11434",
		"ftp://127.0.0.1:11434",
	} {
		if err := ValidateSafeOllamaHostOverride(forbidden); err == nil {
			t.Fatalf("expected ValidateSafeOllamaHostOverride(%q) to reject SSRF target", forbidden)
		}
	}
}

func TestWave16GrepExtractAndErrorPatternRefusals(t *testing.T) {
	t.Parallel()

	badRegex, err := GetGrep(GrepOptions{Pattern: "[unclosed", Session: "missing"})
	if err != nil || badRegex.Success || badRegex.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG on malformed regex, got %+v (%v)", badRegex, err)
	}

	noSession, err := GetGrep(GrepOptions{Pattern: "panic"})
	if err != nil || noSession.Success || noSession.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG when session and --all omitted, got %+v (%v)", noSession, err)
	}

	emptyExtract, err := GetExtract(ExtractOptions{})
	if err != nil || emptyExtract.Success || emptyExtract.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG on empty extract session, got %+v (%v)", emptyExtract, err)
	}

	if matched, _ := MatchErrorLine("    at render (/srv/app.js:12:4)"); matched {
		t.Fatal("MatchErrorLine without stackframes should ignore standalone stack frames")
	}
	if matched, kind := MatchErrorLineWithOptions("    at render (/srv/app.js:12:4)", true); !matched || kind != "stacktrace" {
		t.Fatalf("MatchErrorLineWithOptions(..., true) = (%v, %q), want (true, stacktrace)", matched, kind)
	}
}
