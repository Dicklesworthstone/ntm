package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestShouldStartInternalMonitor_IsDisabledUnderGoTest(t *testing.T) {
	if shouldStartInternalMonitor() {
		t.Fatal("expected internal monitor to be disabled under go test")
	}
}

func TestResolveSpawnProjectDirUsesOverride(t *testing.T) {
	projectDir := t.TempDir()

	got, err := resolveSpawnProjectDir(SpawnOptions{
		Session:            "resume-target",
		ProjectDirOverride: projectDir,
	})
	if err != nil {
		t.Fatalf("resolveSpawnProjectDir() error = %v", err)
	}
	if got != projectDir {
		t.Fatalf("project dir = %q, want %q", got, projectDir)
	}
}

func TestResolveSpawnProjectDirRejectsRelativeOverride(t *testing.T) {
	_, err := resolveSpawnProjectDir(SpawnOptions{
		Session:            "resume-target",
		ProjectDirOverride: "relative/project",
	})
	if err == nil {
		t.Fatal("expected relative override error")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("expected absolute-path error, got %v", err)
	}
}

func TestMonitorProcessPattern_MatchesExactSessionOnly(t *testing.T) {
	pattern := regexp.MustCompile(monitorProcessPatternForExecutable("/usr/local/bin/ntm-dev", "proj"))

	if !pattern.MatchString("/usr/local/bin/ntm-dev internal-monitor proj") {
		t.Fatal("expected exact executable/session monitor command to match")
	}
	if pattern.MatchString("/usr/local/bin/ntm-dev internal-monitor proj2") {
		t.Fatal("expected prefix-sharing session name not to match")
	}
	if pattern.MatchString("/usr/local/bin/ntm-dev2 internal-monitor proj") {
		t.Fatal("expected prefix-sharing executable name not to match")
	}
}

func TestSpawnSessionLogic(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-projects")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize global cfg (unexported in cli package, but accessible here)
	// Save/Restore to prevent side effects
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true

	// Override templates to avoid dependency on actual agent binaries while
	// remaining compatible with explicit model overrides under test.
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCatCommandTemplate
	cfg.Agents.Gemini = testAgentCatCommandTemplate

	// Unique session name
	sessionName := fmt.Sprintf("ntm-test-spawn-%d", time.Now().UnixNano())

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Define agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "claude-3-5-sonnet-20241022"},
	}

	// Pre-create project directory to avoid interactive prompt
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Execute spawn
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}
	err = spawnSessionLogic(opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Validate session exists
	if !tmux.SessionExists(sessionName) {
		t.Errorf("session %s was not created", sessionName)
	}

	// Validate panes
	// Expected: 1 user pane + 1 claude pane = 2 panes
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	if len(panes) != 2 {
		t.Errorf("expected 2 panes, got %d", len(panes))
	}

	// Validate user pane and agent pane
	foundClaude := false
	for _, p := range panes {
		if p.Type == tmux.AgentClaude {
			foundClaude = true
			// Check title format: session__type_index_variant
			expectedTitle := fmt.Sprintf("%s__cc_1_claude-3-5-sonnet-20241022", sessionName)
			if p.Title != expectedTitle {
				t.Errorf("expected pane title %q, got %q", expectedTitle, p.Title)
			}
		}
	}

	if !foundClaude {
		t.Error("did not find Claude agent pane")
	}

	// Verify project directory creation
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		t.Errorf("project directory %s was not created", projectDir)
	}
}

func TestAppendOllamaAgentSpecs(t *testing.T) {
	t.Parallel()

	t.Run("no_agents_noop", func(t *testing.T) {
		var specs AgentSpecs
		model, err := appendOllamaAgentSpecs(&specs, 0, 0, "  codellama:latest  ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if model != "codellama:latest" {
			t.Fatalf("model=%q, want %q", model, "codellama:latest")
		}
		if len(specs) != 0 {
			t.Fatalf("specs len=%d, want 0", len(specs))
		}
	})

	t.Run("local_count_appends_ollama_spec", func(t *testing.T) {
		var specs AgentSpecs
		model, err := appendOllamaAgentSpecs(&specs, 2, 0, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if model != "codellama:latest" {
			t.Fatalf("model=%q, want %q", model, "codellama:latest")
		}
		if len(specs) != 1 {
			t.Fatalf("specs len=%d, want 1", len(specs))
		}
		if specs[0].Type != AgentTypeOllama || specs[0].Count != 2 || specs[0].Model != "codellama:latest" {
			t.Fatalf("spec=%+v, want type=%q count=2 model=%q", specs[0], AgentTypeOllama, "codellama:latest")
		}
	})

	t.Run("ollama_alias_appends_ollama_spec", func(t *testing.T) {
		var specs AgentSpecs
		model, err := appendOllamaAgentSpecs(&specs, 0, 3, "deepseek-coder:33b")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if model != "deepseek-coder:33b" {
			t.Fatalf("model=%q, want %q", model, "deepseek-coder:33b")
		}
		if len(specs) != 1 {
			t.Fatalf("specs len=%d, want 1", len(specs))
		}
		if specs[0].Type != AgentTypeOllama || specs[0].Count != 3 || specs[0].Model != "deepseek-coder:33b" {
			t.Fatalf("spec=%+v, want type=%q count=3 model=%q", specs[0], AgentTypeOllama, "deepseek-coder:33b")
		}
	})

	t.Run("cannot_use_local_and_ollama_together", func(t *testing.T) {
		var specs AgentSpecs
		if _, err := appendOllamaAgentSpecs(&specs, 1, 1, "codellama:latest"); err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("invalid_model_rejected", func(t *testing.T) {
		var specs AgentSpecs
		if _, err := appendOllamaAgentSpecs(&specs, 1, 0, "bad model!"); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestParseLocalFallbackProvider(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		input   string
		want    AgentType
		wantErr bool
	}{
		{name: "default_empty", input: "", want: AgentTypeCodex},
		{name: "cod", input: "cod", want: AgentTypeCodex},
		{name: "codex", input: "codex", want: AgentTypeCodex},
		{name: "codex_cli", input: "codex_cli", want: AgentTypeCodex},
		{name: "codex_dash_cli", input: "codex-cli", want: AgentTypeCodex},
		{name: "openai_codex", input: "openai-codex", want: AgentTypeCodex},
		{name: "cc", input: "cc", want: AgentTypeClaude},
		{name: "claude", input: "claude", want: AgentTypeClaude},
		{name: "claude_code", input: "claude_code", want: AgentTypeClaude},
		{name: "claude_dash_code", input: "claude-code", want: AgentTypeClaude},
		{name: "gmi", input: "gmi", want: AgentTypeGemini},
		{name: "gemini", input: "gemini", want: AgentTypeGemini},
		{name: "gemini_cli", input: "gemini_cli", want: AgentTypeGemini},
		{name: "gemini_dash_cli", input: "gemini-cli", want: AgentTypeGemini},
		{name: "google_gemini", input: "google-gemini", want: AgentTypeGemini},
		{name: "mixed_case_spacing", input: "  CodEx-Cli  ", want: AgentTypeCodex},
		{name: "invalid", input: "ollama", wantErr: true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseLocalFallbackProvider(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (provider=%q)", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("provider=%q => %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestHandleOllamaPreflightError_FallbackDisabled(t *testing.T) {
	t.Parallel()

	opts := SpawnOptions{
		Agents: []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"}},
	}
	expectedErr := fmt.Errorf("connect failed")
	applied, msg, err := handleOllamaPreflightError(&opts, expectedErr)
	if applied {
		t.Fatal("expected fallback not applied")
	}
	if msg != "" {
		t.Fatalf("msg=%q, want empty", msg)
	}
	if err == nil || !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("unexpected err=%v", err)
	}
}

func TestHandleOllamaPreflightError_FallbackEnabledReindexesAndRecounts(t *testing.T) {
	t.Parallel()

	opts := SpawnOptions{
		Agents: []FlatAgent{
			{Type: AgentTypeClaude, Index: 1, Model: "opus"},
			{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"},
			{Type: AgentTypeCodex, Index: 1, Model: "o3"},
			{Type: AgentTypeOllama, Index: 2, Model: "deepseek-coder:6.7b"},
		},
		LocalHost:             "http://localhost:11434",
		LocalFallback:         true,
		LocalFallbackProvider: AgentTypeCodex,
		CCCount:               1,
		CodCount:              1,
		GmiCount:              0,
		CursorCount:           0,
		WindsurfCount:         0,
		AiderCount:            0,
	}

	applied, msg, err := handleOllamaPreflightError(&opts, fmt.Errorf("failed to connect to Ollama"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !applied {
		t.Fatal("expected fallback to be applied")
	}
	if !strings.Contains(msg, "falling back 2 local agent(s) to cod") {
		t.Fatalf("unexpected message: %q", msg)
	}
	if opts.LocalHost != "" {
		t.Fatalf("LocalHost=%q, want empty", opts.LocalHost)
	}

	if len(opts.Agents) != 4 {
		t.Fatalf("agents len=%d, want 4", len(opts.Agents))
	}
	if opts.Agents[1].Type != AgentTypeCodex || opts.Agents[1].Index != 1 || opts.Agents[1].Model != "" {
		t.Fatalf("agent[1]=%+v, want codex index=1 empty model", opts.Agents[1])
	}
	if opts.Agents[2].Type != AgentTypeCodex || opts.Agents[2].Index != 2 {
		t.Fatalf("agent[2]=%+v, want codex index=2", opts.Agents[2])
	}
	if opts.Agents[3].Type != AgentTypeCodex || opts.Agents[3].Index != 3 || opts.Agents[3].Model != "" {
		t.Fatalf("agent[3]=%+v, want codex index=3 empty model", opts.Agents[3])
	}

	if opts.CCCount != 1 || opts.CodCount != 3 || opts.GmiCount != 0 {
		t.Fatalf("counts cc=%d cod=%d gmi=%d, want 1/3/0", opts.CCCount, opts.CodCount, opts.GmiCount)
	}
}

func TestHandleOllamaPreflightError_NoOllamaAgentsStillFails(t *testing.T) {
	t.Parallel()

	opts := SpawnOptions{
		Agents:                []FlatAgent{{Type: AgentTypeClaude, Index: 1, Model: "sonnet"}},
		LocalFallback:         true,
		LocalFallbackProvider: AgentTypeCodex,
	}

	applied, msg, err := handleOllamaPreflightError(&opts, fmt.Errorf("failed to connect to Ollama"))
	if applied {
		t.Fatal("expected fallback not applied")
	}
	if msg != "" {
		t.Fatalf("msg=%q, want empty", msg)
	}
	if err == nil || !strings.Contains(err.Error(), "failed to connect to Ollama") {
		t.Fatalf("unexpected err=%v", err)
	}
}

func withStdinInput(t *testing.T, input string, fn func()) {
	t.Helper()

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("failed to write stdin input: %v", err)
	}
	_ = w.Close()
	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		_ = r.Close()
	}()

	fn()
}

func TestPreflightOllamaSpawn_ModelPresent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{
						"name":   "codellama:latest",
						"size":   0,
						"digest": "sha256:deadbeef",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = true

	host, err := preflightOllamaSpawn(SpawnOptions{
		Agents:     []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"}},
		LocalHost:  server.URL,
		LocalModel: "codellama:latest",
	})
	if err != nil {
		t.Fatalf("preflightOllamaSpawn failed: %v", err)
	}
	if host != strings.TrimSuffix(server.URL, "/") {
		t.Fatalf("host=%q, want %q", host, strings.TrimSuffix(server.URL, "/"))
	}
}

func TestPreflightOllamaSpawn_MissingModel_JSONModeErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{
						"name": "llama3:latest",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = true

	_, err := preflightOllamaSpawn(SpawnOptions{
		Agents:    []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"}},
		LocalHost: server.URL,
	})
	if err == nil {
		t.Fatal("expected missing-model error")
	}
	if !strings.Contains(err.Error(), "not found at") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflightOllamaSpawn_MissingModel_TextModePullsOnConfirm(t *testing.T) {
	var pullCalled atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{}})
		case "/api/pull":
			pullCalled.Store(true)
			flusher, _ := w.(http.Flusher)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "pulling"})
			flusher.Flush()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success"})
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = false

	withStdinInput(t, "y\n", func() {
		host, err := preflightOllamaSpawn(SpawnOptions{
			Agents:    []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "deepseek-coder:6.7b"}},
			LocalHost: server.URL,
		})
		if err != nil {
			t.Fatalf("preflightOllamaSpawn failed: %v", err)
		}
		if host != strings.TrimSuffix(server.URL, "/") {
			t.Fatalf("host=%q, want %q", host, strings.TrimSuffix(server.URL, "/"))
		}
	})

	if !pullCalled.Load() {
		t.Fatal("expected /api/pull to be called after confirmation")
	}
}

func TestPreflightOllamaSpawn_MissingModel_TextModeDecline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = false

	withStdinInput(t, "n\n", func() {
		_, err := preflightOllamaSpawn(SpawnOptions{
			Agents:    []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "deepseek-coder:6.7b"}},
			LocalHost: server.URL,
		})
		if err == nil {
			t.Fatal("expected decline error")
		}
		if !strings.Contains(err.Error(), "not found (try: ollama pull") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSpawnSessionLogic_Ollama(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-projects")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Mock Ollama server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{
						"name":        "codellama:latest",
						"size":        0,
						"digest":      "sha256:deadbeef",
						"modified_at": time.Now().UTC().Format(time.RFC3339),
						"details": map[string]any{
							"format": "gguf",
							"family": "llama",
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Initialize global cfg (unexported in cli package, but accessible here)
	// Save/Restore to prevent side effects
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true

	// Override templates to avoid dependency on actual agent binaries while
	// remaining compatible with explicit model overrides under test.
	cfg.Agents.Ollama = testAgentCatCommandTemplate

	// Unique session name
	sessionName := fmt.Sprintf("ntm-test-spawn-ollama-%d", time.Now().UnixNano())

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Pre-create project directory to avoid interactive prompt
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	opts := SpawnOptions{
		Session:       sessionName,
		Agents:        []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"}},
		UserPane:      true,
		LocalHost:     server.URL,
		LocalModel:    "codellama:latest",
		CCCount:       0,
		CodCount:      0,
		GmiCount:      0,
		CursorCount:   0,
		WindsurfCount: 0,
		AiderCount:    0,
	}

	if err := spawnSessionLogic(opts); err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	if !tmux.SessionExists(sessionName) {
		t.Fatalf("session %s was not created", sessionName)
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(panes))
	}

	foundOllama := false
	for _, p := range panes {
		if p.Type.String() != "ollama" {
			continue
		}
		foundOllama = true
		expectedTitle := fmt.Sprintf("%s__ollama_1_codellama:latest", sessionName)
		if p.Title != expectedTitle {
			t.Errorf("expected pane title %q, got %q", expectedTitle, p.Title)
		}
	}
	if !foundOllama {
		t.Fatal("did not find Ollama agent pane")
	}

	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		t.Fatalf("project directory %s was not created", projectDir)
	}
}

// bd-3f53: Tests for getMemoryContext and formatMemoryContext

func TestFormatMemoryContext_Nil(t *testing.T) {
	t.Parallel()

	result := formatMemoryContext(nil)
	if result != "" {
		t.Errorf("formatMemoryContext(nil) = %q, want empty string", result)
	}
}

func TestFormatMemoryContext_EmptyResult(t *testing.T) {
	t.Parallel()

	result := formatMemoryContext(&cm.CLIContextResponse{
		Success:         true,
		Task:            "test task",
		RelevantBullets: []cm.CLIRule{},
		AntiPatterns:    []cm.CLIRule{},
	})
	if result != "" {
		t.Errorf("formatMemoryContext(empty) = %q, want empty string", result)
	}
}

func TestFormatMemoryContext_RulesOnly(t *testing.T) {
	t.Parallel()

	resp := &cm.CLIContextResponse{
		Success: true,
		Task:    "test task",
		RelevantBullets: []cm.CLIRule{
			{ID: "b-8f3a2c", Content: "Always use structured logging with log/slog", Category: "best-practice"},
			{ID: "b-4e1d7b", Content: "Database migrations must be idempotent", Category: "database"},
		},
		AntiPatterns: []cm.CLIRule{},
	}

	result := formatMemoryContext(resp)

	// Check header
	if !strings.Contains(result, "# Project Memory from Past Sessions") {
		t.Error("missing main header")
	}

	// Check rules section
	if !strings.Contains(result, "## Key Rules for This Project") {
		t.Error("missing Key Rules section header")
	}

	// Check rule formatting
	if !strings.Contains(result, "[b-8f3a2c] Always use structured logging with log/slog") {
		t.Error("missing first rule")
	}
	if !strings.Contains(result, "[b-4e1d7b] Database migrations must be idempotent") {
		t.Error("missing second rule")
	}

	// Should NOT have anti-patterns section
	if strings.Contains(result, "## Anti-Patterns to Avoid") {
		t.Error("should not have Anti-Patterns section when empty")
	}
}

func TestFormatMemoryContext_AntiPatternsOnly(t *testing.T) {
	t.Parallel()

	resp := &cm.CLIContextResponse{
		Success:         true,
		Task:            "test task",
		RelevantBullets: []cm.CLIRule{},
		AntiPatterns: []cm.CLIRule{
			{ID: "b-7d3e8c", Content: "Don't add backwards-compatibility shims", Category: "anti-pattern"},
		},
	}

	result := formatMemoryContext(resp)

	// Check header
	if !strings.Contains(result, "# Project Memory from Past Sessions") {
		t.Error("missing main header")
	}

	// Should NOT have rules section
	if strings.Contains(result, "## Key Rules for This Project") {
		t.Error("should not have Key Rules section when empty")
	}

	// Check anti-patterns section
	if !strings.Contains(result, "## Anti-Patterns to Avoid") {
		t.Error("missing Anti-Patterns section header")
	}
	if !strings.Contains(result, "[b-7d3e8c] Don't add backwards-compatibility shims") {
		t.Error("missing anti-pattern")
	}
}

func TestFormatMemoryContext_BothSections(t *testing.T) {
	t.Parallel()

	resp := &cm.CLIContextResponse{
		Success: true,
		Task:    "test task",
		RelevantBullets: []cm.CLIRule{
			{ID: "b-rule1", Content: "Use Go 1.25 features", Category: "best-practice"},
		},
		AntiPatterns: []cm.CLIRule{
			{ID: "b-anti1", Content: "Avoid using deprecated APIs", Category: "anti-pattern"},
		},
	}

	result := formatMemoryContext(resp)

	// Check both sections present
	if !strings.Contains(result, "## Key Rules for This Project") {
		t.Error("missing Key Rules section")
	}
	if !strings.Contains(result, "## Anti-Patterns to Avoid") {
		t.Error("missing Anti-Patterns section")
	}

	// Check both items present
	if !strings.Contains(result, "[b-rule1]") {
		t.Error("missing rule ID")
	}
	if !strings.Contains(result, "[b-anti1]") {
		t.Error("missing anti-pattern ID")
	}

	// Check order: rules should come before anti-patterns
	rulesIdx := strings.Index(result, "## Key Rules")
	antiIdx := strings.Index(result, "## Anti-Patterns")
	if rulesIdx > antiIdx {
		t.Error("Key Rules should appear before Anti-Patterns")
	}
}

func TestGetMemoryContext_ConfigDisabled(t *testing.T) {
	t.Parallel()

	// Save and restore global config
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	// Create config with CM memories disabled
	cfg = config.Default()
	cfg.SessionRecovery.IncludeCMMemories = false

	result := getMemoryContext("test-project", "test task")
	if result != "" {
		t.Errorf("getMemoryContext with disabled config = %q, want empty string", result)
	}
}

func TestGetMemoryContext_NilConfig(t *testing.T) {
	t.Parallel()

	// Save and restore global config
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	cfg = nil

	result := getMemoryContext("test-project", "test task")
	if result != "" {
		t.Errorf("getMemoryContext with nil config = %q, want empty string", result)
	}
}

func TestGetMemoryContext_EmptyTask(t *testing.T) {
	t.Parallel()

	// Save and restore global config
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	cfg = config.Default()
	cfg.SessionRecovery.IncludeCMMemories = true

	// This test verifies the function handles empty task gracefully
	// Even if CM is not installed, it should return empty string without error
	result := getMemoryContext("test-project", "")

	// Result should be empty (CM likely not installed in test environment)
	// but the function should not panic
	_ = result // Just verify no panic
}

func TestLegacySpawnTotalAgentCount_IncludesOllama(t *testing.T) {
	t.Parallel()

	opts := SpawnOptions{
		CCCount:       1,
		CodCount:      2,
		GmiCount:      3,
		CursorCount:   4,
		WindsurfCount: 5,
		AiderCount:    6,
		OllamaCount:   7,
	}

	if got := legacySpawnTotalAgentCount(opts); got != 28 {
		t.Fatalf("legacySpawnTotalAgentCount() = %d, want 28", got)
	}
}

func TestSpawnHookCountEnv_IncludesOllama(t *testing.T) {
	t.Parallel()

	env := spawnHookCountEnv(5, SpawnOptions{OllamaCount: 2, CursorCount: 3})
	if env["NTM_AGENT_COUNT_OLLAMA"] != "2" {
		t.Fatalf("NTM_AGENT_COUNT_OLLAMA = %q, want 2", env["NTM_AGENT_COUNT_OLLAMA"])
	}
	if env["NTM_AGENT_COUNT_TOTAL"] != "5" {
		t.Fatalf("NTM_AGENT_COUNT_TOTAL = %q, want 5", env["NTM_AGENT_COUNT_TOTAL"])
	}
}

func TestSpawnSessionCreatedEventFields_IncludeModernTypes(t *testing.T) {
	t.Parallel()

	fields := spawnSessionCreatedEventFields(SpawnOptions{
		RecipeName:    "default",
		CCCount:       1,
		CursorCount:   2,
		WindsurfCount: 3,
		AiderCount:    4,
		OllamaCount:   5,
	}, "/tmp/project")

	if fields["agent_count"] != "15" {
		t.Fatalf("agent_count = %q, want 15", fields["agent_count"])
	}
	if fields["agent_cursor"] != "2" || fields["agent_windsurf"] != "3" || fields["agent_aider"] != "4" || fields["agent_ollama"] != "5" {
		t.Fatalf("spawnSessionCreatedEventFields() missing modern counts: %+v", fields)
	}
}

func TestNormalizeSpawnOptions_ExpandsLegacyCountsIncludingOllama(t *testing.T) {
	t.Parallel()

	opts := SpawnOptions{
		CursorCount: 2,
		OllamaCount: 1,
	}

	normalizeSpawnOptions(&opts)

	if len(opts.Agents) != 3 {
		t.Fatalf("len(opts.Agents) = %d, want 3", len(opts.Agents))
	}
	if opts.CursorCount != 2 {
		t.Fatalf("CursorCount = %d, want 2", opts.CursorCount)
	}
	if opts.OllamaCount != 1 {
		t.Fatalf("OllamaCount = %d, want 1", opts.OllamaCount)
	}
	if opts.Agents[0].Type != AgentTypeCursor || opts.Agents[0].Index != 1 {
		t.Fatalf("agent[0] = %+v, want cursor_1", opts.Agents[0])
	}
	if opts.Agents[1].Type != AgentTypeCursor || opts.Agents[1].Index != 2 {
		t.Fatalf("agent[1] = %+v, want cursor_2", opts.Agents[1])
	}
	if opts.Agents[2].Type != AgentTypeOllama || opts.Agents[2].Index != 1 {
		t.Fatalf("agent[2] = %+v, want ollama_1", opts.Agents[2])
	}
}

func TestNormalizeSpawnOptions_RecomputesModernCountsFromAgents(t *testing.T) {
	t.Parallel()

	opts := SpawnOptions{
		Agents: []FlatAgent{
			{Type: AgentTypeCursor, Index: 1},
			{Type: AgentTypeWindsurf, Index: 1},
			{Type: AgentTypeOllama, Index: 1},
			{Type: AgentTypeOllama, Index: 2},
		},
	}

	normalizeSpawnOptions(&opts)

	if opts.CCCount != 0 || opts.CodCount != 0 || opts.GmiCount != 0 {
		t.Fatalf("legacy cloud counts changed unexpectedly: cc=%d cod=%d gmi=%d", opts.CCCount, opts.CodCount, opts.GmiCount)
	}
	if opts.CursorCount != 1 {
		t.Fatalf("CursorCount = %d, want 1", opts.CursorCount)
	}
	if opts.WindsurfCount != 1 {
		t.Fatalf("WindsurfCount = %d, want 1", opts.WindsurfCount)
	}
	if opts.OllamaCount != 2 {
		t.Fatalf("OllamaCount = %d, want 2", opts.OllamaCount)
	}
}

func TestProfileAssignmentWarning(t *testing.T) {
	t.Parallel()

	if msg := profileAssignmentWarning(0, 3); msg != "" {
		t.Fatalf("profileAssignmentWarning(0, 3) = %q, want empty", msg)
	}
	if msg := profileAssignmentWarning(2, 2); msg != "" {
		t.Fatalf("profileAssignmentWarning(2, 2) = %q, want empty", msg)
	}
	if msg := profileAssignmentWarning(2, 5); msg != "Warning: 2 profiles for 5 agents; profiles will be assigned in order" {
		t.Fatalf("profileAssignmentWarning(2, 5) = %q", msg)
	}
}

func TestWizardAgentSpecs_IncludesModernTypes(t *testing.T) {
	t.Parallel()

	specs := wizardAgentSpecs(SpawnWizardResult{
		CCCount:       1,
		CursorCount:   2,
		WindsurfCount: 1,
		AiderCount:    1,
		OllamaCount:   3,
	})

	if len(specs) != 5 {
		t.Fatalf("len(specs) = %d, want 5", len(specs))
	}
	if specs[0] != (AgentSpec{Type: AgentTypeClaude, Count: 1}) {
		t.Fatalf("specs[0] = %+v, want Claude x1", specs[0])
	}
	if specs[1] != (AgentSpec{Type: AgentTypeCursor, Count: 2}) {
		t.Fatalf("specs[1] = %+v, want Cursor x2", specs[1])
	}
	if specs[2] != (AgentSpec{Type: AgentTypeWindsurf, Count: 1}) {
		t.Fatalf("specs[2] = %+v, want Windsurf x1", specs[2])
	}
	if specs[3] != (AgentSpec{Type: AgentTypeAider, Count: 1}) {
		t.Fatalf("specs[3] = %+v, want Aider x1", specs[3])
	}
	if specs[4] != (AgentSpec{Type: AgentTypeOllama, Count: 3}) {
		t.Fatalf("specs[4] = %+v, want Ollama x3", specs[4])
	}
}

func TestSpawnWizardResultFromCounts_IncludesModernTypes(t *testing.T) {
	t.Parallel()

	result := spawnWizardResultFromCounts(map[string]int{
		"cc":       1,
		"cursor":   2,
		"windsurf": 3,
		"aider":    4,
		"ollama":   5,
	})

	if result.CCCount != 1 || result.CursorCount != 2 || result.WindsurfCount != 3 || result.AiderCount != 4 || result.OllamaCount != 5 {
		t.Fatalf("spawnWizardResultFromCounts() = %+v", result)
	}
}

func TestFormatWizardAgentCountSummary_IncludesModernTypes(t *testing.T) {
	t.Parallel()

	got := formatWizardAgentCountSummary(map[string]int{
		"cc":       1,
		"cursor":   2,
		"windsurf": 1,
		"aider":    1,
		"ollama":   3,
	})
	want := "cc:1 cursor:2 windsurf:1 aider:1 ollama:3"
	if got != want {
		t.Fatalf("formatWizardAgentCountSummary() = %q, want %q", got, want)
	}
	if got := formatWizardAgentCountSummary(nil); got != "no agents" {
		t.Fatalf("formatWizardAgentCountSummary(nil) = %q, want %q", got, "no agents")
	}
}

func TestWizardLaunchAgentSpecs_ManualWizardKeepsCounts(t *testing.T) {
	t.Parallel()

	specs := wizardLaunchAgentSpecs(SpawnWizardResult{
		CCCount:       1,
		CursorCount:   2,
		WindsurfCount: 1,
	})

	if len(specs) != 3 {
		t.Fatalf("len(specs) = %d, want 3", len(specs))
	}
	if specs[0] != (AgentSpec{Type: AgentTypeClaude, Count: 1}) {
		t.Fatalf("specs[0] = %+v, want Claude x1", specs[0])
	}
	if specs[1] != (AgentSpec{Type: AgentTypeCursor, Count: 2}) {
		t.Fatalf("specs[1] = %+v, want Cursor x2", specs[1])
	}
	if specs[2] != (AgentSpec{Type: AgentTypeWindsurf, Count: 1}) {
		t.Fatalf("specs[2] = %+v, want Windsurf x1", specs[2])
	}
}

func TestWizardLaunchAgentSpecs_RecipeAndTemplateSelectionsDeferCounts(t *testing.T) {
	t.Parallel()

	if specs := wizardLaunchAgentSpecs(SpawnWizardResult{CCCount: 2, Recipe: "review-team"}); len(specs) != 0 {
		t.Fatalf("recipe selection specs = %+v, want empty", specs)
	}
	if specs := wizardLaunchAgentSpecs(SpawnWizardResult{CodCount: 1, Template: "red-green"}); len(specs) != 0 {
		t.Fatalf("template selection specs = %+v, want empty", specs)
	}
	if !wizardDeferredSelection(SpawnWizardResult{Recipe: "review-team"}) {
		t.Fatal("wizardDeferredSelection(recipe) = false, want true")
	}
	if !wizardDeferredSelection(SpawnWizardResult{Template: "red-green"}) {
		t.Fatal("wizardDeferredSelection(template) = false, want true")
	}
	if wizardDeferredSelection(SpawnWizardResult{CCCount: 1}) {
		t.Fatal("wizardDeferredSelection(manual) = true, want false")
	}
}
