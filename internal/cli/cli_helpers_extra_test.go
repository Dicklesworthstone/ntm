package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

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
	}

	outputs := collectSummaryAgentOutputs(panes, func(id string, lines int) (string, error) {
		if lines != 500 {
			t.Fatalf("capture lines = %d, want 500", lines)
		}
		return "out:" + id, nil
	}, nil)

	if len(outputs) != 2 {
		t.Fatalf("collectSummaryAgentOutputs() returned %d outputs, want 2", len(outputs))
	}
	if outputs[0].AgentID != "%1" || outputs[0].AgentType != "claude" {
		t.Fatalf("first output = %+v, want claude %%1", outputs[0])
	}
	if outputs[1].AgentID != "%3" || outputs[1].AgentType != "codex" {
		t.Fatalf("second output = %+v, want codex %%3", outputs[1])
	}
}

func TestCollectReadyAgentPanesUsesParsedPaneType(t *testing.T) {

	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude, Title: "notes"},
		{ID: "%2", Index: 2, Type: tmux.AgentUser, Title: "claude-shell"},
		{ID: "%3", Index: 3, Type: tmux.AgentType("openai-codex"), Title: "custom"},
	}

	outputs := map[string]string{
		"%1": "claude> ",
		"%2": "$ ",
		"%3": "codex> ",
	}

	ready, totalAgents := collectReadyAgentPanes(panes, func(id string) (string, error) {
		return outputs[id], nil
	})

	if totalAgents != 2 {
		t.Fatalf("collectReadyAgentPanes() totalAgents = %d, want 2", totalAgents)
	}
	if len(ready) != 2 {
		t.Fatalf("collectReadyAgentPanes() returned %d ready panes, want 2", len(ready))
	}
	if ready[0].ID != "%1" || ready[1].ID != "%3" {
		t.Fatalf("ready panes = %+v, want %%1 and %%3", ready)
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

func TestGenerateRecommendationsUsesParsedPaneType(t *testing.T) {

	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude, Title: "notes"},
		{ID: "%2", Index: 2, Type: tmux.AgentUser, Title: "project__cc_2"},
		{ID: "%3", Index: 3, Type: tmux.AgentType("openai-codex"), Title: "custom"},
	}

	beads := []struct {
		id    string
		title string
	}{
		{id: "bd-1", title: "Fix auth"},
		{id: "bd-2", title: "Review queue"},
	}

	recs := generateRecommendations(
		panes,
		[]bv.BeadPreview{
			{ID: beads[0].id, Title: beads[0].title},
			{ID: beads[1].id, Title: beads[1].title},
		},
		"balanced",
		[]string{"1", "3"},
	)

	if len(recs) != 2 {
		t.Fatalf("generateRecommendations() returned %d recs, want 2", len(recs))
	}
	if recs[0].AgentType != "claude" || recs[0].Agent != "1" {
		t.Fatalf("first recommendation = %+v, want pane 1 claude", recs[0])
	}
	if recs[1].AgentType != "codex" || recs[1].Agent != "3" {
		t.Fatalf("second recommendation = %+v, want pane 3 codex", recs[1])
	}
}

func TestIncrementAgentCounts(t *testing.T) {

	counts := output.AgentCountsResponse{}
	incrementAgentCounts(&counts, tmux.AgentClaude)
	incrementAgentCounts(&counts, tmux.AgentOllama)
	incrementAgentCounts(&counts, tmux.AgentType("openai-codex"))
	incrementAgentCounts(&counts, tmux.AgentUser)
	incrementAgentCounts(&counts, tmux.AgentType("mystery"))

	if counts.Claude != 1 {
		t.Fatalf("Claude = %d, want 1", counts.Claude)
	}
	if counts.Ollama != 1 {
		t.Fatalf("Ollama = %d, want 1", counts.Ollama)
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
	if counts.Total != 5 {
		t.Fatalf("Total = %d, want 5", counts.Total)
	}
}

func TestDashboardPaneTypeSummary(t *testing.T) {

	panes := []tmux.Pane{
		{Type: tmux.AgentClaude},
		{Type: tmux.AgentOllama},
		{Type: tmux.AgentCursor},
		{Type: tmux.AgentWindsurf},
		{Type: tmux.AgentAider},
		{Type: tmux.AgentUser},
		{Type: tmux.AgentType("openai-codex")},
		{Type: tmux.AgentUnknown},
	}

	got := dashboardPaneTypeSummary(panes)
	want := "Claude=1 Codex=1 Gemini=0 Cursor=1 Windsurf=1 Aider=1 Opencode=0 Ollama=1 User=1 Other=1"
	if got != want {
		t.Fatalf("dashboardPaneTypeSummary() = %q, want %q", got, want)
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
	updateAgentStats(breakdown, "claude", 1, 5, 100)
	if s := breakdown["claude"]; s.Count != 1 || s.Prompts != 5 || s.TokensEst != 100 {
		t.Errorf("after first update: %+v", s)
	}

	// Second update accumulates
	updateAgentStats(breakdown, "claude", 2, 3, 200)
	if s := breakdown["claude"]; s.Count != 3 || s.Prompts != 8 || s.TokensEst != 300 {
		t.Errorf("after second update: %+v", s)
	}

	// Different agent type
	updateAgentStats(breakdown, "codex", 1, 1, 50)
	if s := breakdown["codex"]; s.Count != 1 || s.Prompts != 1 || s.TokensEst != 50 {
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

	if val.Type() != "N[:model]" {
		t.Errorf("Type() = %q, want %q", val.Type(), "N[:model]")
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
