package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
