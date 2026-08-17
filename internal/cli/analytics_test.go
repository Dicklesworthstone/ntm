package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
)

func TestReadEvents(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	// Write test events
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now.AddDate(0, 0, -5), Type: events.EventSessionCreate, Session: "test1", Data: map[string]interface{}{"claude_count": float64(2), "codex_count": float64(1)}},
		{Timestamp: now.AddDate(0, 0, -3), Type: events.EventPromptSend, Session: "test1", Data: map[string]interface{}{"prompt_length": float64(100), "target_types": "cc"}},
		{Timestamp: now.AddDate(0, 0, -40), Type: events.EventSessionCreate, Session: "old", Data: map[string]interface{}{"claude_count": float64(1)}},
	}

	var data []byte
	for _, e := range testEvents {
		line, _ := json.Marshal(e)
		data = append(data, line...)
		data = append(data, '\n')
	}

	if err := os.WriteFile(logPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Test reading with 30-day cutoff
	cutoff := now.AddDate(0, 0, -30)
	eventList, err := readEvents(logPath, cutoff)
	if err != nil {
		t.Fatalf("readEvents failed: %v", err)
	}

	// Should have 2 events (not the old one)
	if len(eventList) != 2 {
		t.Errorf("Got %d events, want 2", len(eventList))
	}
}

func TestAggregateStats(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -30)

	testEvents := []events.Event{
		{Timestamp: now.AddDate(0, 0, -5), Type: events.EventSessionCreate, Session: "test1", Data: map[string]interface{}{"claude_count": float64(2), "codex_count": float64(1)}},
		{Timestamp: now.AddDate(0, 0, -3), Type: events.EventPromptSend, Session: "test1", Data: map[string]interface{}{"prompt_length": float64(100), "target_types": "cc"}},
		{Timestamp: now.AddDate(0, 0, -2), Type: events.EventPromptSend, Session: "test1", Data: map[string]interface{}{"prompt_length": float64(200), "target_types": "all"}},
		{Timestamp: now.AddDate(0, 0, -1), Type: events.EventError, Session: "test1", Data: map[string]interface{}{"error_type": "spawn_failed"}},
	}

	stats := aggregateStats(testEvents, 30, "", cutoff)

	if stats.TotalSessions != 1 {
		t.Errorf("TotalSessions = %d, want 1", stats.TotalSessions)
	}

	if stats.TotalAgents != 3 {
		t.Errorf("TotalAgents = %d, want 3", stats.TotalAgents)
	}

	if stats.TotalPrompts != 2 {
		t.Errorf("TotalPrompts = %d, want 2", stats.TotalPrompts)
	}

	if stats.TotalCharsSent != 300 {
		t.Errorf("TotalCharsSent = %d, want 300", stats.TotalCharsSent)
	}

	if stats.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", stats.ErrorCount)
	}

	// Check agent breakdown
	if claude, ok := stats.AgentBreakdown["claude"]; ok {
		if claude.Count != 2 {
			t.Errorf("claude.Count = %d, want 2", claude.Count)
		}
	} else {
		t.Error("Missing claude in agent breakdown")
	}
}

func TestParseTargetTypes(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"cc", []string{"claude"}},
		{"cod", []string{"codex"}},
		{"gmi", []string{"gemini"}},
		{"xai_grok_build", []string{"grok"}},
		{"cc,cod", []string{"claude", "codex"}},
		{"all", []string{"claude", "codex", "gemini", "grok"}},
		{"agents", []string{"claude", "codex", "gemini", "grok"}},
		{"", []string{}},
	}

	for _, tt := range tests {
		result := parseTargetTypes(tt.input)
		if len(result) != len(tt.expected) {
			t.Fatalf("parseTargetTypes(%q) = %v, want %v", tt.input, result, tt.expected)
		}
		for i, want := range tt.expected {
			if result[i] != want {
				t.Errorf("parseTargetTypes(%q)[%d] = %q, want %q", tt.input, i, result[i], want)
			}
		}
	}
}

func TestBuildSessionDetails(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now.AddDate(0, 0, -5), Type: events.EventSessionCreate, Session: "test1", Data: map[string]interface{}{"claude_count": float64(2)}},
		{Timestamp: now.AddDate(0, 0, -3), Type: events.EventPromptSend, Session: "test1", Data: map[string]interface{}{}},
		{Timestamp: now.AddDate(0, 0, -2), Type: events.EventPromptSend, Session: "test1", Data: map[string]interface{}{}},
		{Timestamp: now.AddDate(0, 0, -1), Type: events.EventSessionCreate, Session: "test2", Data: map[string]interface{}{"codex_count": float64(1)}},
	}

	details := buildSessionDetails(testEvents)

	if len(details) != 2 {
		t.Errorf("Got %d sessions, want 2", len(details))
	}

	// Test2 should be first (more recent)
	if len(details) > 0 && details[0].Name != "test2" {
		t.Errorf("First session = %q, want 'test2'", details[0].Name)
	}

	// Test1 should have 2 prompts
	for _, d := range details {
		if d.Name == "test1" && d.PromptCount != 2 {
			t.Errorf("test1 prompts = %d, want 2", d.PromptCount)
		}
	}
}

func TestAggregateStats_EmptyEvents(t *testing.T) {
	stats := aggregateStats(nil, 30, "", time.Now())
	if stats.TotalSessions != 0 {
		t.Errorf("TotalSessions = %d, want 0", stats.TotalSessions)
	}
	if stats.TotalPrompts != 0 {
		t.Errorf("TotalPrompts = %d, want 0", stats.TotalPrompts)
	}
	if len(stats.AgentBreakdown) != 0 {
		t.Errorf("AgentBreakdown should be empty, got %d entries", len(stats.AgentBreakdown))
	}
}

func TestAggregateStats_SincePeriod(t *testing.T) {
	now := time.Now().UTC()
	stats := aggregateStats([]events.Event{
		{Timestamp: now, Type: events.EventSessionCreate, Session: "s1", Data: map[string]interface{}{"claude_count": float64(1)}},
	}, 0, "2026-01-15", now.AddDate(0, 0, -30))

	if stats.Period != "Since 2026-01-15" {
		t.Errorf("Period = %q, want 'Since 2026-01-15'", stats.Period)
	}
}

func TestAggregateStats_EstimatedTokensField(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{
			"prompt_length":    float64(350),
			"estimated_tokens": float64(100),
			"target_types":     "cc",
		}},
	}

	stats := aggregateStats(testEvents, 30, "", now.AddDate(0, 0, -30))

	// Should use estimated_tokens (100), not fallback from prompt_length (350*10/35 = 100)
	if stats.TotalTokensEst != 100 {
		t.Errorf("TotalTokensEst = %d, want 100", stats.TotalTokensEst)
	}
}

func TestAggregateStats_TokenFallbackFromPromptLength(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{
			"prompt_length": float64(350),
			"target_types":  "cc",
		}},
	}

	stats := aggregateStats(testEvents, 30, "", now.AddDate(0, 0, -30))

	// Without estimated_tokens, fallback: 350 * 10 / 35 = 100
	if stats.TotalTokensEst != 100 {
		t.Errorf("TotalTokensEst = %d, want 100 (fallback from prompt_length)", stats.TotalTokensEst)
	}
}

func TestAggregateStats_TokenDistributionAcrossTargets(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{
			"estimated_tokens": float64(300),
			"prompt_length":    float64(1000),
			"target_types":     "cc,cod,gmi",
		}},
	}

	stats := aggregateStats(testEvents, 30, "", now.AddDate(0, 0, -30))

	// 300 tokens / 3 targets = 100 per target
	for _, agentType := range []string{"claude", "codex", "gemini"} {
		agent, ok := stats.AgentBreakdown[agentType]
		if !ok {
			t.Errorf("Missing %s in agent breakdown", agentType)
			continue
		}
		if agent.TokensEst != 100 {
			t.Errorf("%s.TokensEst = %d, want 100", agentType, agent.TokensEst)
		}
		if agent.Prompts != 1 {
			t.Errorf("%s.Prompts = %d, want 1", agentType, agent.Prompts)
		}
	}
}

func TestAggregateStats_MultipleErrorTypes(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventError, Session: "s1", Data: map[string]interface{}{"error_type": "spawn_failed"}},
		{Timestamp: now, Type: events.EventError, Session: "s1", Data: map[string]interface{}{"error_type": "spawn_failed"}},
		{Timestamp: now, Type: events.EventError, Session: "s1", Data: map[string]interface{}{"error_type": "timeout"}},
	}

	stats := aggregateStats(testEvents, 30, "", now.AddDate(0, 0, -30))

	if stats.ErrorCount != 3 {
		t.Errorf("ErrorCount = %d, want 3", stats.ErrorCount)
	}
	if stats.ErrorTypes["spawn_failed"] != 2 {
		t.Errorf("ErrorTypes['spawn_failed'] = %d, want 2", stats.ErrorTypes["spawn_failed"])
	}
	if stats.ErrorTypes["timeout"] != 1 {
		t.Errorf("ErrorTypes['timeout'] = %d, want 1", stats.ErrorTypes["timeout"])
	}
}

func TestAggregateStats_MultipleSessions(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now.AddDate(0, 0, -5), Type: events.EventSessionCreate, Session: "sess-a", Data: map[string]interface{}{"claude_count": float64(2)}},
		{Timestamp: now.AddDate(0, 0, -3), Type: events.EventSessionCreate, Session: "sess-b", Data: map[string]interface{}{"codex_count": float64(1), "gemini_count": float64(1)}},
	}

	stats := aggregateStats(testEvents, 30, "", now.AddDate(0, 0, -30))

	if stats.TotalSessions != 2 {
		t.Errorf("TotalSessions = %d, want 2", stats.TotalSessions)
	}
	if stats.TotalAgents != 4 {
		t.Errorf("TotalAgents = %d, want 4", stats.TotalAgents)
	}
}

func TestAggregateStats_GeminiAgents(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventSessionCreate, Session: "s1", Data: map[string]interface{}{"gemini_count": float64(3)}},
	}

	stats := aggregateStats(testEvents, 30, "", now.AddDate(0, 0, -30))

	gemini, ok := stats.AgentBreakdown["gemini"]
	if !ok {
		t.Fatal("Missing gemini in agent breakdown")
	}
	if gemini.Count != 3 {
		t.Errorf("gemini.Count = %d, want 3", gemini.Count)
	}
}

func TestAggregateStats_GrokAgents(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventSessionCreate, Session: "s1", Data: map[string]interface{}{"grok_count": float64(3)}},
	}

	stats := aggregateStats(testEvents, 30, "", now.AddDate(0, 0, -30))
	if stats.TotalAgents != 3 {
		t.Fatalf("TotalAgents = %d, want 3", stats.TotalAgents)
	}
	grok, ok := stats.AgentBreakdown["grok"]
	if !ok {
		t.Fatal("Missing grok in agent breakdown")
	}
	if grok.Count != 3 {
		t.Fatalf("grok.Count = %d, want 3", grok.Count)
	}
}

func TestParseTargetTypes_FullNames(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"claude", []string{"claude"}},
		{"codex", []string{"codex"}},
		{"gemini", []string{"gemini"}},
		{"grok-build", []string{"grok"}},
		{"CLAUDE,CODEX", []string{"claude", "codex"}},
		{"Claude,Gemini", []string{"claude", "gemini"}},
	}

	for _, tt := range tests {
		result := parseTargetTypes(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("parseTargetTypes(%q) got %d results, want %d: %v", tt.input, len(result), len(tt.expected), result)
		}
	}
}

func TestParseTargetTypes_AllWithExisting(t *testing.T) {
	// When "all" is combined with specific types already matched,
	// it should not add duplicates (the function only adds all if result is empty)
	result := parseTargetTypes("cc,all")

	// "cc" matches claude, then "all" is checked but result is not empty, so no extra addition
	if len(result) != 1 {
		t.Errorf("parseTargetTypes('cc,all') = %v, want [claude] (all skipped since result not empty)", result)
	}
}

func TestParseTargetTypes_NoMatch(t *testing.T) {
	result := parseTargetTypes("unknown_type")
	if len(result) != 0 {
		t.Errorf("parseTargetTypes('unknown_type') = %v, want empty", result)
	}
}

func TestParseTargetTypes_ModernAndAliased(t *testing.T) {
	result := parseTargetTypes("openai-codex, ws, cursor, aider, ollama, codex, pane:3, tags:[backend]")
	expected := []string{"codex", "windsurf", "cursor", "aider", "ollama"}
	if len(result) != len(expected) {
		t.Fatalf("parseTargetTypes returned %d results, want %d: %v", len(result), len(expected), result)
	}
	for i, want := range expected {
		if result[i] != want {
			t.Fatalf("parseTargetTypes result[%d] = %q, want %q (%v)", i, result[i], want, result)
		}
	}
}

func TestAggregateStats_OllamaAgents(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventSessionCreate, Session: "s1", Data: map[string]interface{}{"ollama_count": float64(2)}},
	}

	stats := aggregateStats(testEvents, 30, "", now.AddDate(0, 0, -30))
	if stats.TotalAgents != 2 {
		t.Fatalf("TotalAgents = %d, want 2", stats.TotalAgents)
	}
	ollama, ok := stats.AgentBreakdown["ollama"]
	if !ok {
		t.Fatal("Missing ollama in agent breakdown")
	}
	if ollama.Count != 2 {
		t.Fatalf("ollama.Count = %d, want 2", ollama.Count)
	}
}

func TestBuildSessionDetails_IncludesOllamaAgents(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventSessionCreate, Session: "s1", Data: map[string]interface{}{"codex_count": float64(1), "ollama_count": float64(2)}},
	}

	details := buildSessionDetails(testEvents)
	if len(details) != 1 {
		t.Fatalf("Got %d sessions, want 1", len(details))
	}
	if details[0].AgentCount != 3 {
		t.Fatalf("AgentCount = %d, want 3", details[0].AgentCount)
	}
}

func TestUpdateAgentStats_Cumulative(t *testing.T) {
	breakdown := make(map[string]AgentStats)

	updateAgentStats(breakdown, "claude", 2, 0, 0, 0)
	updateAgentStats(breakdown, "claude", 0, 3, 500, 150)
	updateAgentStats(breakdown, "claude", 0, 1, 250, 70)
	updateAgentStats(breakdown, "codex", 1, 1, 175, 50)

	claude := breakdown["claude"]
	if claude.Count != 2 {
		t.Errorf("claude.Count = %d, want 2", claude.Count)
	}
	if claude.Prompts != 4 {
		t.Errorf("claude.Prompts = %d, want 4", claude.Prompts)
	}
	if claude.CharsSent != 750 {
		t.Errorf("claude.CharsSent = %d, want 750 (must accumulate)", claude.CharsSent)
	}
	if claude.TokensEst != 220 {
		t.Errorf("claude.TokensEst = %d, want 220", claude.TokensEst)
	}

	codex := breakdown["codex"]
	if codex.Count != 1 {
		t.Errorf("codex.Count = %d, want 1", codex.Count)
	}
}

func TestBuildSessionDetails_EmptyEvents(t *testing.T) {
	details := buildSessionDetails(nil)
	if len(details) != 0 {
		t.Errorf("Got %d sessions, want 0", len(details))
	}
}

func TestBuildSessionDetails_SkipsEmptySession(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventSessionCreate, Session: "", Data: map[string]interface{}{}},
		{Timestamp: now, Type: events.EventSessionCreate, Session: "valid", Data: map[string]interface{}{"claude_count": float64(1)}},
	}

	details := buildSessionDetails(testEvents)
	if len(details) != 1 {
		t.Errorf("Got %d sessions, want 1 (empty session name should be skipped)", len(details))
	}
	if len(details) > 0 && details[0].Name != "valid" {
		t.Errorf("Session name = %q, want 'valid'", details[0].Name)
	}
}

func TestBuildSessionDetails_AgentCounts(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventSessionCreate, Session: "s1", Data: map[string]interface{}{
			"claude_count": float64(2),
			"codex_count":  float64(1),
			"gemini_count": float64(3),
			"grok_count":   float64(4),
		}},
	}

	details := buildSessionDetails(testEvents)
	if len(details) != 1 {
		t.Fatalf("Got %d sessions, want 1", len(details))
	}
	if details[0].AgentCount != 10 {
		t.Errorf("AgentCount = %d, want 10", details[0].AgentCount)
	}
}

func TestBuildSessionDetails_SortOrder(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now.AddDate(0, 0, -10), Type: events.EventSessionCreate, Session: "old", Data: map[string]interface{}{}},
		{Timestamp: now.AddDate(0, 0, -5), Type: events.EventSessionCreate, Session: "mid", Data: map[string]interface{}{}},
		{Timestamp: now, Type: events.EventSessionCreate, Session: "new", Data: map[string]interface{}{}},
	}

	details := buildSessionDetails(testEvents)
	if len(details) != 3 {
		t.Fatalf("Got %d sessions, want 3", len(details))
	}
	// Most recent first
	if details[0].Name != "new" {
		t.Errorf("First session = %q, want 'new'", details[0].Name)
	}
	if details[1].Name != "mid" {
		t.Errorf("Second session = %q, want 'mid'", details[1].Name)
	}
	if details[2].Name != "old" {
		t.Errorf("Third session = %q, want 'old'", details[2].Name)
	}
}
