package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hookspkg "github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func decodeSingleTerminalJSONMap(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	document := make(map[string]interface{})
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode terminal JSON: %v; output=%q", err, raw)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout is not exactly one JSON document: %v; output=%q", err, raw)
	}
	return document
}

func chdirForTerminalJSONTest(t *testing.T, dir string) {
	t.Helper()
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("change working directory to %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore working directory to %s: %v", originalDir, err)
		}
	})
}

// =============================================================================
// activity.go: detectAgentTypeFromPane
// =============================================================================

func TestDetectAgentTypeFromPane(t *testing.T) {

	tests := []struct {
		name string
		pane tmux.Pane
		want string
	}{
		{"claude", tmux.Pane{Type: tmux.AgentClaude}, "claude"},
		{"codex", tmux.Pane{Type: tmux.AgentCodex}, "codex"},
		{"gemini", tmux.Pane{Type: tmux.AgentGemini}, "gemini"},
		{"grok alias canonicalized", tmux.Pane{Type: tmux.AgentType("xai_grok_build")}, "grok"},
		{"cursor", tmux.Pane{Type: tmux.AgentCursor}, "cursor"},
		{"windsurf", tmux.Pane{Type: tmux.AgentWindsurf}, "windsurf"},
		{"aider", tmux.Pane{Type: tmux.AgentAider}, "aider"},
		{"ollama", tmux.Pane{Type: tmux.AgentOllama}, "ollama"},
		{"codex alias canonicalized", tmux.Pane{Type: tmux.AgentType("openai-codex")}, "codex"},
		{"user", tmux.Pane{Type: tmux.AgentUser}, "user"},
		{"unknown", tmux.Pane{Type: tmux.AgentUnknown}, "unknown"},
		{"empty type", tmux.Pane{Type: ""}, "unknown"},
		{"arbitrary", tmux.Pane{Type: "something-else"}, "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectAgentTypeFromPane(tc.pane)
			if got != tc.want {
				t.Errorf("detectAgentTypeFromPane(%v) = %q, want %q", tc.pane.Type, got, tc.want)
			}
		})
	}
}

func TestAgentTypeForPanePrefersTmuxTypeAndFallsBackToTitle(t *testing.T) {

	tests := []struct {
		name string
		pane tmux.Pane
		want string
	}{
		{"tmux type wins over custom title", tmux.Pane{Type: tmux.AgentClaude, Title: "notes"}, "claude"},
		{"tmux user type wins over misleading title", tmux.Pane{Type: tmux.AgentUser, Title: "project__cc_1"}, "user"},
		{"falls back to legacy title parsing", tmux.Pane{Type: "", Title: "project__cod_2"}, "codex"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentTypeForPane(tc.pane); got != tc.want {
				t.Errorf("agentTypeForPane(%+v) = %q, want %q", tc.pane, got, tc.want)
			}
		})
	}
}

func TestCollectSummaryAgentOutputsUsesParsedPaneType(t *testing.T) {

	panes := []tmux.Pane{
		{ID: "%1", Type: tmux.AgentClaude, Title: "custom-title"},
		{ID: "%2", Type: tmux.AgentUser, Title: "claude-looking-shell"},
		{ID: "%3", Type: tmux.AgentType("openai-codex"), Title: "other-title"},
		{ID: "%4", Type: tmux.AgentType("grok-build"), Title: "custom-grok-title"},
	}

	outputs := collectSummaryAgentOutputs(panes, func(id string, lines int) (string, error) {
		if lines != 500 {
			t.Fatalf("capture lines = %d, want 500", lines)
		}
		return "out:" + id, nil
	}, nil)

	if len(outputs) != 3 {
		t.Fatalf("collectSummaryAgentOutputs() returned %d outputs, want 3", len(outputs))
	}
	if outputs[0].AgentID != "%1" || outputs[0].AgentType != "claude" {
		t.Fatalf("first output = %+v, want claude %%1", outputs[0])
	}
	if outputs[1].AgentID != "%3" || outputs[1].AgentType != "codex" {
		t.Fatalf("second output = %+v, want codex %%3", outputs[1])
	}
	if outputs[2].AgentID != "%4" || outputs[2].AgentType != "grok" {
		t.Fatalf("third output = %+v, want grok %%4", outputs[2])
	}
}

func TestPaneTitleTypeAndIndex(t *testing.T) {

	tests := []struct {
		name  string
		title string
		wantT string
		wantN int
		want  bool
	}{
		{"simple", "project__cc_2", "cc", 2, true},
		{"embedded double underscore session", "my__project__cursor_4", "cursor", 4, true},
		{"variant preserved before index parse", "my__project__cod_3_opus", "cod", 3, true},
		{"tagged pane title", "my__project__cc_7[frontend,api]", "cc", 7, true},
		{"claude alias title canonicalized", "my__project__claude_5", "cc", 5, true},
		{"codex alias title canonicalized", "my__project__openai-codex_6", "cod", 6, true},
		{"invalid", "custom-title", "", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotNum, gotOK := paneTitleTypeAndIndex(tc.title)
			if gotType != tc.wantT || gotNum != tc.wantN || gotOK != tc.want {
				t.Fatalf("paneTitleTypeAndIndex(%q) = (%q, %d, %v), want (%q, %d, %v)", tc.title, gotType, gotNum, gotOK, tc.wantT, tc.wantN, tc.want)
			}
		})
	}
}

func TestIncrementAgentCounts(t *testing.T) {

	counts := output.AgentCountsResponse{}
	incrementAgentCounts(&counts, tmux.AgentClaude)
	incrementAgentCounts(&counts, tmux.AgentOllama)
	incrementAgentCounts(&counts, tmux.AgentGrok)
	incrementAgentCounts(&counts, tmux.AgentType("openai-codex"))
	incrementAgentCounts(&counts, tmux.AgentUser)
	incrementAgentCounts(&counts, tmux.AgentType("mystery"))

	if counts.Claude != 1 {
		t.Fatalf("Claude = %d, want 1", counts.Claude)
	}
	if counts.Ollama != 1 {
		t.Fatalf("Ollama = %d, want 1", counts.Ollama)
	}
	if counts.Grok != 1 {
		t.Fatalf("Grok = %d, want 1", counts.Grok)
	}
	if counts.Codex != 1 {
		t.Fatalf("Codex = %d, want 1", counts.Codex)
	}
	if counts.User != 1 {
		t.Fatalf("User = %d, want 1", counts.User)
	}
	if counts.Other != 1 {
		t.Fatalf("Other = %d, want 1", counts.Other)
	}
	if counts.Total != 6 {
		t.Fatalf("Total = %d, want 6", counts.Total)
	}
}

func TestDashboardPaneTypeSummary(t *testing.T) {
	tests := []struct {
		name  string
		panes []tmux.Pane
		want  string
	}{
		{
			name: "empty",
			want: "Claude=0 Codex=0 Gemini=0 Grok=0 Cursor=0 Windsurf=0 Aider=0 Opencode=0 Ollama=0 User=0 Other=0",
		},
		{
			name: "grok alias",
			panes: []tmux.Pane{
				{Type: tmux.AgentType("grok-build")},
			},
			want: "Claude=0 Codex=0 Gemini=0 Grok=1 Cursor=0 Windsurf=0 Aider=0 Opencode=0 Ollama=0 User=0 Other=0",
		},
		{
			name: "mixed",
			panes: []tmux.Pane{
				{Type: tmux.AgentClaude},
				{Type: tmux.AgentGrok},
				{Type: tmux.AgentOllama},
				{Type: tmux.AgentCursor},
				{Type: tmux.AgentWindsurf},
				{Type: tmux.AgentAider},
				{Type: tmux.AgentUser},
				{Type: tmux.AgentType("openai-codex")},
				{Type: tmux.AgentUnknown},
			},
			want: "Claude=1 Codex=1 Gemini=0 Grok=1 Cursor=1 Windsurf=1 Aider=1 Opencode=0 Ollama=1 User=1 Other=1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dashboardPaneTypeSummary(test.panes); got != test.want {
				t.Fatalf("dashboardPaneTypeSummary() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPaneOutputPrefixColor(t *testing.T) {

	current := theme.Current()
	tests := []struct {
		name      string
		agentType tmux.AgentType
		want      string
	}{
		{"claude", tmux.AgentClaude, string(current.Claude)},
		{"codex alias canonicalized", tmux.AgentType("openai-codex"), string(current.Codex)},
		{"cursor", tmux.AgentCursor, string(current.Cursor)},
		{"windsurf", tmux.AgentWindsurf, string(current.Windsurf)},
		{"aider", tmux.AgentAider, string(current.Aider)},
		{"opencode", tmux.AgentOpencode, string(current.Opencode)},
		{"ollama", tmux.AgentOllama, string(current.Ollama)},
		{"user", tmux.AgentUser, string(current.User)},
		{"unknown fallback", tmux.AgentUnknown, string(current.Green)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(paneOutputPrefixColor(tc.agentType, current)); got != tc.want {
				t.Fatalf("paneOutputPrefixColor(%v) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestShortAgentTypeLocal(t *testing.T) {

	tests := []struct {
		name      string
		agentType string
		want      string
	}{
		{"claude", "claude", "cc"},
		{"codex alias", "openai-codex", "cod"},
		{"gemini alias", "google-gemini", "gmi"},
		{"cursor", "cursor", "cur"},
		{"windsurf alias", "ws", "ws"},
		{"aider", "aider", "aid"},
		{"opencode short", "oc", "oc"},
		{"opencode long", "opencode", "oc"},
		{"ollama", "ollama", "oll"},
		{"user", "user", "usr"},
		{"unknown", "mystery", "mys"},
		{"empty", "", "unk"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shortAgentTypeLocal(tc.agentType); got != tc.want {
				t.Fatalf("shortAgentTypeLocal(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestLogsAgentTypeColor(t *testing.T) {

	current := theme.Current()
	tests := []struct {
		name      string
		agentType string
		want      string
	}{
		{"claude", "claude", string(current.Claude)},
		{"codex alias", "openai-codex", string(current.Codex)},
		{"gemini alias", "google-gemini", string(current.Gemini)},
		{"cursor", "cursor", string(current.Cursor)},
		{"windsurf", "windsurf", string(current.Windsurf)},
		{"aider", "aider", string(current.Aider)},
		{"opencode short", "oc", string(current.Opencode)},
		{"opencode long", "opencode", string(current.Opencode)},
		{"ollama", "ollama", string(current.Ollama)},
		{"user", "user", string(current.User)},
		{"unknown", "mystery", string(current.Text)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(logsAgentTypeColor(tc.agentType, current)); got != tc.want {
				t.Fatalf("logsAgentTypeColor(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestAggregatedLogPrefix(t *testing.T) {

	current := theme.Current()
	tests := []struct {
		name      string
		agentType string
		pane      int
		want      string
	}{
		{"claude", "claude", 2, "[cc:2]"},
		{"codex alias", "openai-codex", 3, "[cod:3]"},
		{"windsurf", "windsurf", 4, "[ws:4]"},
		{"user", "user", 5, "[usr:5]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripANSI(aggregatedLogPrefix(tc.agentType, tc.pane, current)); got != tc.want {
				t.Fatalf("aggregatedLogPrefix(%q, %d) = %q, want %q", tc.agentType, tc.pane, got, tc.want)
			}
		})
	}
}

// =============================================================================
// analytics.go: updateAgentStats
// =============================================================================

func TestUpdateAgentStats(t *testing.T) {

	breakdown := make(map[string]AgentStats)

	// First update creates entry
	updateAgentStats(breakdown, "claude", 1, 5, 350, 100)
	if s := breakdown["claude"]; s.Count != 1 || s.Prompts != 5 || s.CharsSent != 350 || s.TokensEst != 100 {
		t.Errorf("after first update: %+v", s)
	}

	// Second update accumulates
	updateAgentStats(breakdown, "claude", 2, 3, 700, 200)
	if s := breakdown["claude"]; s.Count != 3 || s.Prompts != 8 || s.CharsSent != 1050 || s.TokensEst != 300 {
		t.Errorf("after second update: %+v", s)
	}

	// Different agent type
	updateAgentStats(breakdown, "codex", 1, 1, 175, 50)
	if s := breakdown["codex"]; s.Count != 1 || s.Prompts != 1 || s.CharsSent != 175 || s.TokensEst != 50 {
		t.Errorf("codex entry: %+v", s)
	}

	// Original unchanged
	if s := breakdown["claude"]; s.Count != 3 {
		t.Errorf("claude changed unexpectedly: %+v", s)
	}
}

// =============================================================================
// redaction_io.go: formatRedactionCategoryCounts
// =============================================================================

func TestFormatRedactionCategoryCounts(t *testing.T) {

	tests := []struct {
		name       string
		categories map[string]int
		want       string
	}{
		{"nil", nil, ""},
		{"empty", map[string]int{}, ""},
		{"single", map[string]int{"PASSWORD": 3}, "PASSWORD=3"},
		{"multiple sorted", map[string]int{"TOKEN": 2, "API_KEY": 1, "PASSWORD": 3}, "API_KEY=1, PASSWORD=3, TOKEN=2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatRedactionCategoryCounts(tc.categories)
			if got != tc.want {
				t.Errorf("formatRedactionCategoryCounts(%v) = %q, want %q", tc.categories, got, tc.want)
			}
		})
	}
}

// =============================================================================
// redaction_io.go: redactionBlockedError.Error()
// =============================================================================

func TestRedactionBlockedError(t *testing.T) {

	// With categories
	err := redactionBlockedError{
		summary: RedactionSummary{
			Categories: map[string]int{"PASSWORD": 2, "API_KEY": 1},
		},
	}
	msg := err.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
	if !strings.Contains(msg, "PASSWORD") {
		t.Errorf("expected error to mention PASSWORD, got %q", msg)
	}
	if !strings.Contains(msg, "API_KEY") {
		t.Errorf("expected error to mention API_KEY, got %q", msg)
	}

	// Without categories
	errEmpty := redactionBlockedError{
		summary: RedactionSummary{},
	}
	msgEmpty := errEmpty.Error()
	if !strings.Contains(msgEmpty, "refusing to proceed") {
		t.Errorf("expected 'refusing to proceed', got %q", msgEmpty)
	}
}

// =============================================================================
// health.go: truncateString
// =============================================================================

func TestTruncateStringHealth(t *testing.T) {

	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hell…"},
		{"maxLen 1", "hello", 1, "h"},
		{"maxLen 0", "hello", 0, ""},
		{"empty string", "", 5, ""},
		{"unicode", "日本語テスト", 4, "日本語…"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateString(tc.s, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tc.s, tc.maxLen, got, tc.want)
			}
		})
	}
}

// =============================================================================
// agent_spec.go: AgentSpecsValue.Set, Type
// =============================================================================

func TestAgentSpecsValue_SetAndType(t *testing.T) {

	var specs AgentSpecs
	val := NewAgentSpecsValue(AgentTypeClaude, &specs)

	if val.Type() != "N[:model[:effort]]" {
		t.Errorf("Type() = %q, want %q", val.Type(), "N[:model[:effort]]")
	}

	if err := val.Set("3"); err != nil {
		t.Fatalf("Set(3) error: %v", err)
	}
	if len(specs) != 1 || specs[0].Count != 3 {
		t.Errorf("after Set(3): specs = %+v", specs)
	}

	if err := val.Set("2:opus"); err != nil {
		t.Fatalf("Set(2:opus) error: %v", err)
	}
	if len(specs) != 2 || specs[1].Count != 2 || specs[1].Model != "opus" {
		t.Errorf("after Set(2:opus): specs = %+v", specs)
	}
}

// =============================================================================
// agent_spec.go: AgentSpecs.String
// =============================================================================

func TestAgentSpecsStringFormatting(t *testing.T) {

	tests := []struct {
		name  string
		specs AgentSpecs
		want  string
	}{
		{"empty", AgentSpecs{}, ""},
		{"single", AgentSpecs{{Type: AgentTypeClaude, Count: 1}}, "1"},
		{"with model", AgentSpecs{{Type: AgentTypeClaude, Count: 2, Model: "opus"}}, "2:opus"},
		{"multiple", AgentSpecs{
			{Type: AgentTypeClaude, Count: 1},
			{Type: AgentTypeCodex, Count: 3, Model: "gpt4"},
		}, "1,3:gpt4"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.specs.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAppendMissingCountMapAgentSpecs_CanonicalizesModernTypes(t *testing.T) {

	specs := AgentSpecs{{Type: AgentTypeCursor, Count: 1, Model: "existing"}}
	appendMissingCountMapAgentSpecs(&specs, map[string]int{
		"cursor":       4,
		"openai-codex": 3,
		"ws":           2,
		"ollama":       1,
	})

	if got := specs.ByType(AgentTypeCursor).TotalCount(); got != 1 {
		t.Fatalf("cursor count = %d, want 1 existing override", got)
	}
	if got := specs.ByType(AgentTypeCodex).TotalCount(); got != 3 {
		t.Fatalf("codex count = %d, want 3", got)
	}
	if got := specs.ByType(AgentTypeWindsurf).TotalCount(); got != 2 {
		t.Fatalf("windsurf count = %d, want 2", got)
	}
	if got := specs.ByType(AgentTypeOllama).TotalCount(); got != 1 {
		t.Fatalf("ollama count = %d, want 1", got)
	}
}

func TestAppendMissingRecipeAgentSpecs_PreservesModelsAndPersonas(t *testing.T) {

	specs := AgentSpecs{}
	personaMap := map[string]*persona.Persona{}
	err := appendMissingRecipeAgentSpecs(&specs, personaMap, "review-team", "", []recipe.AgentSpec{
		{Type: "openai-codex", Count: 2, Model: "gpt-5"},
		{Type: "claude", Count: 1, Persona: "architect", Model: "sonnet"},
		{Type: "ws", Count: 3},
		{Type: "xai_grok_build", Count: 1, Model: "grok-4", ReasoningEffort: "high"},
	})
	if err != nil {
		t.Fatalf("appendMissingRecipeAgentSpecs error: %v", err)
	}

	codex := specs.ByType(AgentTypeCodex)
	if len(codex) != 1 || codex[0].Count != 2 || codex[0].Model != "gpt-5" {
		t.Fatalf("codex specs = %+v, want count=2 model=gpt-5", codex)
	}

	claude := specs.ByType(AgentTypeClaude)
	if len(claude) != 1 || claude[0].Count != 1 || claude[0].Model == "" {
		t.Fatalf("claude specs = %+v, want persona-backed model key", claude)
	}
	personaCfg, ok := personaMap[claude[0].Model]
	if !ok {
		t.Fatalf("personaMap missing key %q", claude[0].Model)
	}
	if personaCfg.Name != "architect" {
		t.Fatalf("persona name = %q, want architect", personaCfg.Name)
	}
	if personaCfg.Model != "sonnet" {
		t.Fatalf("persona model = %q, want sonnet override", personaCfg.Model)
	}

	if got := specs.ByType(AgentTypeWindsurf).TotalCount(); got != 3 {
		t.Fatalf("windsurf count = %d, want 3", got)
	}

	grok := specs.ByType(AgentTypeGrok)
	if len(grok) != 1 || grok[0].Count != 1 || grok[0].Model != "grok-4" || grok[0].ReasoningEffort != "high" {
		t.Fatalf("Grok specs = %+v, want count=1 model=grok-4 effort=high", grok)
	}
}

func TestFormatSpawnCountSummaryAndRecipeLabels_CanonicalizeAliases(t *testing.T) {

	got := formatSpawnCountSummary(map[string]int{
		"openai-codex": 2,
		"ws":           1,
		"cursor":       1,
	})
	if got != "2 cod, 1 cursor, 1 windsurf" {
		t.Fatalf("formatSpawnCountSummary() = %q", got)
	}

	if label := formatAgentTypeSimple("google-gemini"); label != "Gemini" {
		t.Fatalf("formatAgentTypeSimple(google-gemini) = %q, want Gemini", label)
	}
	if label := formatAgentTypeSimple("ws"); label != "Windsurf" {
		t.Fatalf("formatAgentTypeSimple(ws) = %q, want Windsurf", label)
	}
}

func TestDepsJSONMissingRequiredReturnsTerminalFailure(t *testing.T) {
	originalJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = originalJSON })
	t.Setenv("PATH", t.TempDir())

	stdout, runErr := captureStdout(t, func() error { return runDeps(false) })
	assertTerminalJSONFailureContains(t, stdout, runErr, "required dependencies are missing")
	document := decodeSingleTerminalJSONMap(t, stdout)
	assertTypedTerminalJSONFailure(t, document, robot.ErrCodeDependencyMissing)
	if installed, ok := document["all_installed"].(bool); !ok || installed {
		t.Fatalf("all_installed = %#v, want false", document["all_installed"])
	}
	if dependencies, ok := document["dependencies"].([]interface{}); !ok || len(dependencies) == 0 {
		t.Fatalf("dependencies = %#v, want non-empty array", document["dependencies"])
	}
}

func TestRecipesJSONFailuresAreTerminal(t *testing.T) {
	originalJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = originalJSON })

	t.Run("loader error", func(t *testing.T) {
		configHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)
		chdirForTerminalJSONTest(t, t.TempDir())
		configDir := filepath.Join(configHome, "ntm")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			t.Fatalf("create recipe config directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "recipes.toml"), []byte("[[recipes]\n"), 0644); err != nil {
			t.Fatalf("write invalid recipes file: %v", err)
		}

		stdout, runErr := captureStdout(t, runRecipesList)
		assertTerminalJSONFailureContains(t, stdout, runErr, "parsing ")
	})

	t.Run("missing name", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		chdirForTerminalJSONTest(t, t.TempDir())

		stdout, runErr := captureStdout(t, func() error {
			return runRecipesShow("definitely-missing-recipe")
		})
		assertTerminalJSONFailureContains(t, stdout, runErr, "recipe not found")
	})
}

func TestHooksStatusJSONFailureIsTerminal(t *testing.T) {
	originalJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = originalJSON })

	t.Run("manager error", func(t *testing.T) {
		chdirForTerminalJSONTest(t, t.TempDir())
		stdout, runErr := captureStdout(t, runHooksStatus)
		assertTerminalJSONFailureContains(t, stdout, runErr, "not a git repository")
	})

	t.Run("status read error", func(t *testing.T) {
		gitPath, err := exec.LookPath("git")
		if err != nil {
			t.Skip("git is required for hook status fixture")
		}
		repoDir := t.TempDir()
		if output, err := exec.CommandContext(t.Context(), gitPath, "init", "-q", repoDir).CombinedOutput(); err != nil {
			t.Fatalf("initialize git repository: %v: %s", err, output)
		}
		chdirForTerminalJSONTest(t, repoDir)
		if err := os.Mkdir(filepath.Join(repoDir, ".git", "hooks", "pre-commit"), 0755); err != nil {
			t.Fatalf("create unreadable hook fixture: %v", err)
		}

		stdout, runErr := captureStdout(t, runHooksStatus)
		assertTerminalJSONFailureContains(t, stdout, runErr, "reading hook")
	})
}

func TestPreCommitHookJSONBlockedResultIsTerminal(t *testing.T) {
	result := &hookspkg.PreCommitResult{
		Passed:      false,
		StagedFiles: []string{"internal/cli/hooks.go"},
		BlockReason: "critical issues exceeded threshold",
	}

	stdout, runErr := captureStdout(t, func() error { return outputPreCommitHookJSON(result) })
	assertTerminalJSONFailureContains(t, stdout, runErr, result.BlockReason)
	document := decodeSingleTerminalJSONMap(t, stdout)
	if passed, ok := document["passed"].(bool); !ok || passed {
		t.Fatalf("passed = %#v, want false", document["passed"])
	}
}

func TestPreCommitHookJSONPassedResultIsSuccess(t *testing.T) {
	result := &hookspkg.PreCommitResult{
		Passed:       true,
		StagedFiles:  []string{},
		UBSAvailable: true,
	}

	stdout, runErr := captureStdout(t, func() error { return outputPreCommitHookJSON(result) })
	if runErr != nil {
		t.Fatalf("outputPreCommitHookJSON() error = %v, want nil", runErr)
	}
	document := decodeSingleTerminalJSONMap(t, stdout)
	if success, ok := document["success"].(bool); !ok || !success {
		t.Fatalf("success = %#v, want true", document["success"])
	}
	if passed, ok := document["passed"].(bool); !ok || !passed {
		t.Fatalf("passed = %#v, want true", document["passed"])
	}
}

func TestBugsJSONUnavailableStatesAreTerminal(t *testing.T) {
	originalJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = originalJSON })
	t.Setenv("PATH", t.TempDir())
	projectDir := t.TempDir()

	tests := []struct {
		name          string
		run           func() error
		want          string
		wantErrorCode string
	}{
		{
			name: "list without UBS",
			run: func() error {
				cmd := newBugsListCmd()
				return cmd.RunE(cmd, []string{projectDir})
			},
			want:          "ubs is not installed",
			wantErrorCode: robot.ErrCodeDependencyMissing,
		},
		{
			name: "notify without UBS",
			run: func() error {
				cmd := newBugsNotifyCmd()
				return cmd.RunE(cmd, []string{projectDir})
			},
			want: "ubs is not installed",
		},
		{
			name: "summary without cache",
			run: func() error {
				cmd := newBugsSummaryCmd()
				return cmd.RunE(cmd, []string{projectDir})
			},
			want: "scan_cache.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, runErr := captureStdout(t, tt.run)
			assertTerminalJSONFailureContains(t, stdout, runErr, tt.want)
			document := decodeSingleTerminalJSONMap(t, stdout)
			if tt.wantErrorCode != "" {
				assertTypedTerminalJSONFailure(t, document, tt.wantErrorCode)
			}
			if available, ok := document["available"].(bool); !ok || available {
				t.Fatalf("available = %#v, want false", document["available"])
			}
		})
	}
}

func assertTypedTerminalJSONFailure(t *testing.T, document map[string]interface{}, wantCode string) {
	t.Helper()
	if code, ok := document["error_code"].(string); !ok || code != wantCode {
		t.Fatalf("error_code = %#v, want %q", document["error_code"], wantCode)
	}
	if hint, ok := document["hint"].(string); !ok || strings.TrimSpace(hint) == "" {
		t.Fatalf("hint = %#v, want non-empty remediation", document["hint"])
	}
	timestamp, ok := document["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp = %#v, want RFC3339 string", document["timestamp"])
	}
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
		t.Fatalf("timestamp %q is not RFC3339: %v", timestamp, err)
	}
	if format, ok := document["output_format"].(string); !ok || format != "json" {
		t.Fatalf("output_format = %#v, want json", document["output_format"])
	}
}

func assertTerminalJSONFailureContains(t *testing.T, stdout string, runErr error, want string) {
	t.Helper()
	if !errors.Is(runErr, errJSONFailure) {
		t.Fatalf("error = %v, want errJSONFailure", runErr)
	}
	if !strings.Contains(runErr.Error(), want) {
		t.Fatalf("error = %v, want %q", runErr, want)
	}
	document := decodeSingleTerminalJSONMap(t, stdout)
	if success, ok := document["success"].(bool); !ok || success {
		t.Fatalf("success = %#v, want false", document["success"])
	}
}

// bd-ws7-docs-ux-truth-tqh3l.5 proof: an explicit CLI count for a type no
// longer DROPS the recipe's row for that type — the row's model, effort, and
// persona merge onto the CLI-provided specs (CLI-explicit fields win, recipe
// fills the gaps), while the CLI count stays authoritative.
func TestAppendMissingRecipeAgentSpecs_CLICountMergesRecipeSettings(t *testing.T) {
	specs := AgentSpecs{{Type: AgentTypeClaude, Count: 3}}
	err := appendMissingRecipeAgentSpecs(&specs, map[string]*persona.Persona{}, "team", "", []recipe.AgentSpec{
		{Type: "cc", Count: 1, Model: "sonnet", ReasoningEffort: "high"},
		{Type: "cod", Count: 2, Model: "gpt-5"},
	})
	if err != nil {
		t.Fatalf("appendMissingRecipeAgentSpecs error: %v", err)
	}

	claude := specs.ByType(AgentTypeClaude)
	if len(claude) != 1 || claude[0].Count != 3 {
		t.Fatalf("claude specs = %+v, want CLI count 3 preserved", claude)
	}
	if claude[0].Model != "sonnet" || claude[0].ReasoningEffort != "high" {
		t.Fatalf("claude specs = %+v, want recipe model=sonnet effort=high merged (not dropped)", claude)
	}

	codex := specs.ByType(AgentTypeCodex)
	if len(codex) != 1 || codex[0].Count != 2 || codex[0].Model != "gpt-5" {
		t.Fatalf("codex specs = %+v, want appended count=2 model=gpt-5", codex)
	}
}

func TestAppendMissingRecipeAgentSpecs_CLIExplicitFieldsWinOverRecipe(t *testing.T) {
	specs := AgentSpecs{{Type: AgentTypeCodex, Count: 2, Model: "gpt4"}}
	err := appendMissingRecipeAgentSpecs(&specs, map[string]*persona.Persona{}, "team", "", []recipe.AgentSpec{
		{Type: "cod", Count: 5, Model: "gpt-5", ReasoningEffort: "high"},
	})
	if err != nil {
		t.Fatalf("appendMissingRecipeAgentSpecs error: %v", err)
	}

	codex := specs.ByType(AgentTypeCodex)
	if len(codex) != 1 || codex[0].Count != 2 {
		t.Fatalf("codex specs = %+v, want CLI count 2 preserved over recipe count 5", codex)
	}
	if codex[0].Model != "gpt4" {
		t.Fatalf("codex model = %q, want CLI-explicit gpt4 to win over recipe gpt-5", codex[0].Model)
	}
	if codex[0].ReasoningEffort != "high" {
		t.Fatalf("codex effort = %q, want recipe effort high to fill the gap", codex[0].ReasoningEffort)
	}
}

func TestAppendMissingRecipeAgentSpecs_CLICountMergesRecipePersona(t *testing.T) {
	specs := AgentSpecs{{Type: AgentTypeClaude, Count: 2}}
	personaMap := map[string]*persona.Persona{}
	err := appendMissingRecipeAgentSpecs(&specs, personaMap, "review-team", "", []recipe.AgentSpec{
		{Type: "cc", Count: 1, Persona: "architect", Model: "sonnet"},
	})
	if err != nil {
		t.Fatalf("appendMissingRecipeAgentSpecs error: %v", err)
	}

	claude := specs.ByType(AgentTypeClaude)
	if len(claude) != 1 || claude[0].Count != 2 {
		t.Fatalf("claude specs = %+v, want CLI count 2 preserved", claude)
	}
	if claude[0].Model == "" {
		t.Fatalf("claude specs = %+v, want persona-backed model key after merge", claude)
	}
	personaCfg, ok := personaMap[claude[0].Model]
	if !ok {
		t.Fatalf("personaMap missing key %q after merge", claude[0].Model)
	}
	if personaCfg.Name != "architect" {
		t.Fatalf("persona name = %q, want architect", personaCfg.Name)
	}
	if personaCfg.Model != "sonnet" {
		t.Fatalf("persona model = %q, want sonnet override from recipe row", personaCfg.Model)
	}
}

func TestAppendMissingRecipeAgentSpecs_OnlyFirstDuplicateRowMerges(t *testing.T) {
	specs := AgentSpecs{{Type: AgentTypeClaude, Count: 4}}
	err := appendMissingRecipeAgentSpecs(&specs, map[string]*persona.Persona{}, "team", "", []recipe.AgentSpec{
		{Type: "cc", Count: 1, Model: "sonnet"},
		{Type: "cc", Count: 2, Model: "opus"},
	})
	if err != nil {
		t.Fatalf("appendMissingRecipeAgentSpecs error: %v", err)
	}

	claude := specs.ByType(AgentTypeClaude)
	if len(claude) != 1 || claude[0].Count != 4 || claude[0].Model != "sonnet" {
		t.Fatalf("claude specs = %+v, want single spec count=4 model=sonnet (first row merges, second skipped)", claude)
	}
}
