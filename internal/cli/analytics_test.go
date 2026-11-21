package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
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
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}

	if err := os.WriteFile(logPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Test reading with 30-day cutoff
	cutoff := now.AddDate(0, 0, -30)
	eventList, err := events.ReadSince(logPath, cutoff)
	if err != nil {
		t.Fatalf("ReadSince failed: %v", err)
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

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", cutoff)

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
		result := robot.ParseAnalyticsTargetTypes(tt.input)
		if len(result) != len(tt.expected) {
			t.Fatalf("ParseAnalyticsTargetTypes(%q) = %v, want %v", tt.input, result, tt.expected)
		}
		for i, want := range tt.expected {
			if result[i] != want {
				t.Errorf("ParseAnalyticsTargetTypes(%q)[%d] = %q, want %q", tt.input, i, result[i], want)
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

	details := robot.BuildAnalyticsSessionDetails(testEvents)

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
	stats := robot.AggregateAnalyticsEvents(nil, 30, "", time.Now())
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
	stats := robot.AggregateAnalyticsEvents([]events.Event{
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

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))

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

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))

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

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))

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

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))

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

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))

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

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))

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

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))
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
		result := robot.ParseAnalyticsTargetTypes(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("ParseAnalyticsTargetTypes(%q) got %d results, want %d: %v", tt.input, len(result), len(tt.expected), result)
		}
	}
}

func TestParseTargetTypes_AllWithExisting(t *testing.T) {
	// When "all" is combined with specific types already matched,
	// it should not add duplicates (the function only adds all if result is empty)
	result := robot.ParseAnalyticsTargetTypes("cc,all")

	// "cc" matches claude, then "all" is checked but result is not empty, so no extra addition
	if len(result) != 1 {
		t.Errorf("ParseAnalyticsTargetTypes('cc,all') = %v, want [claude] (all skipped since result not empty)", result)
	}
}

func TestParseTargetTypes_NoMatch(t *testing.T) {
	result := robot.ParseAnalyticsTargetTypes("unknown_type")
	if len(result) != 0 {
		t.Errorf("ParseAnalyticsTargetTypes('unknown_type') = %v, want empty", result)
	}
}

func TestParseTargetTypes_ModernAndAliased(t *testing.T) {
	result := robot.ParseAnalyticsTargetTypes("openai-codex, ws, cursor, aider, ollama, codex, pane:3, tags:[backend]")
	expected := []string{"codex", "windsurf", "cursor", "aider", "ollama"}
	if len(result) != len(expected) {
		t.Fatalf("ParseAnalyticsTargetTypes returned %d results, want %d: %v", len(result), len(expected), result)
	}
	for i, want := range expected {
		if result[i] != want {
			t.Fatalf("ParseAnalyticsTargetTypes result[%d] = %q, want %q (%v)", i, result[i], want, result)
		}
	}
}

func TestAggregateStats_OllamaAgents(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventSessionCreate, Session: "s1", Data: map[string]interface{}{"ollama_count": float64(2)}},
	}

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))
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

	details := robot.BuildAnalyticsSessionDetails(testEvents)
	if len(details) != 1 {
		t.Fatalf("Got %d sessions, want 1", len(details))
	}
	if details[0].AgentCount != 3 {
		t.Fatalf("AgentCount = %d, want 3", details[0].AgentCount)
	}
}

func TestUpdateAgentStats_Cumulative(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{
			Timestamp: now,
			Type:      events.EventSessionCreate,
			Session:   "s1",
			Data: map[string]interface{}{
				"claude_count": float64(2),
				"codex_count":  float64(1),
			},
		},
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{"prompt_length": float64(150), "estimated_tokens": float64(50), "target_types": "cc"}},
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{"prompt_length": float64(150), "estimated_tokens": float64(50), "target_types": "cc"}},
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{"prompt_length": float64(200), "estimated_tokens": float64(50), "target_types": "cc"}},
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{"prompt_length": float64(250), "estimated_tokens": float64(70), "target_types": "cc"}},
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{"prompt_length": float64(175), "estimated_tokens": float64(50), "target_types": "cod"}},
	}

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))

	claude := stats.AgentBreakdown["claude"]
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

	codex := stats.AgentBreakdown["codex"]
	if codex.Count != 1 {
		t.Errorf("codex.Count = %d, want 1", codex.Count)
	}
}

func TestBuildSessionDetails_EmptyEvents(t *testing.T) {
	details := robot.BuildAnalyticsSessionDetails(nil)
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

	details := robot.BuildAnalyticsSessionDetails(testEvents)
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

	details := robot.BuildAnalyticsSessionDetails(testEvents)
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

	details := robot.BuildAnalyticsSessionDetails(testEvents)
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

func TestAnalyticsMultiHarnessSessionCreateAndBuildDetails(t *testing.T) {
	now := time.Now().UTC()
	inMemSession := events.ToMap(events.SessionCreateData{
		ClaudeCount:      1,
		CodexCount:       1,
		AntigravityCount: 3,
		OpencodeCount:    2,
	})

	testEvents := []events.Event{
		{
			Timestamp: now.Add(-2 * time.Hour),
			Type:      events.EventSessionCreate,
			Session:   "swarm-inmem",
			Data:      inMemSession,
		},
		{
			Timestamp: now.Add(-1 * time.Hour),
			Type:      events.EventSessionCreate,
			Session:   "swarm-json-alias",
			Data: map[string]interface{}{
				"agy_count": float64(2),
				"oc_count":  float64(4),
			},
		},
	}

	stats := robot.AggregateAnalyticsEvents(testEvents, 7, "", now.AddDate(0, 0, -7))
	if stats.TotalSessions != 2 {
		t.Fatalf("TotalSessions = %d, want 2", stats.TotalSessions)
	}
	if stats.TotalAgents != 13 {
		t.Fatalf("TotalAgents = %d, want 13", stats.TotalAgents)
	}
	if got := stats.AgentBreakdown["antigravity"].Count; got != 5 {
		t.Fatalf("antigravity.Count = %d, want 5", got)
	}
	if got := stats.AgentBreakdown["oc"].Count; got != 6 {
		t.Fatalf("oc.Count = %d, want 6", got)
	}

	details := robot.BuildAnalyticsSessionDetails(testEvents)
	if len(details) != 2 {
		t.Fatalf("len(details) = %d, want 2", len(details))
	}
	if details[0].Name != "swarm-json-alias" || details[0].AgentCount != 6 {
		t.Fatalf("details[0] = %+v, want swarm-json-alias with AgentCount=6", details[0])
	}
	if details[1].Name != "swarm-inmem" || details[1].AgentCount != 7 {
		t.Fatalf("details[1] = %+v, want swarm-inmem with AgentCount=7", details[1])
	}
}

func TestAnalyticsAgentAddAndPromptSendMultiHarness(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now, Type: events.EventAgentAdd, Session: "s1", Data: map[string]interface{}{"agent_type": "agy"}},
		{Timestamp: now, Type: events.EventAgentAdd, Session: "s1", Data: map[string]interface{}{"agent_type": "antigravity"}},
		{Timestamp: now, Type: events.EventAgentAdd, Session: "s1", Data: map[string]interface{}{"agent_type": "oc"}},
		{Timestamp: now, Type: events.EventAgentAdd, Session: "s1", Data: map[string]interface{}{"agent_type": "opencode"}},
		{
			Timestamp: now,
			Type:      events.EventPromptSend,
			Session:   "s1",
			Data: map[string]interface{}{
				"prompt_length":    float64(201),
				"estimated_tokens": float64(60),
				"target_types":     "agy,oc",
			},
		},
	}

	if !isAnalyticsAgentType("antigravity") || !isAnalyticsAgentType("oc") {
		t.Fatal("expected isAnalyticsAgentType to recognize antigravity and oc")
	}

	targets := robot.ParseAnalyticsTargetTypes("agy,opencode,antigravity,oc")
	if len(targets) != 2 || targets[0] != "antigravity" || targets[1] != "oc" {
		t.Fatalf("ParseAnalyticsTargetTypes(agy,opencode,antigravity,oc) = %v, want [antigravity oc]", targets)
	}

	stats := robot.AggregateAnalyticsEvents(testEvents, 30, "", now.AddDate(0, 0, -30))
	if stats.TotalAgents != 4 {
		t.Fatalf("TotalAgents = %d, want 4", stats.TotalAgents)
	}
	agy := stats.AgentBreakdown["antigravity"]
	if agy.Count != 2 || agy.Prompts != 1 || agy.CharsSent != 101 || agy.TokensEst != 30 {
		t.Fatalf("antigravity breakdown = %+v, want {Count:2 Prompts:1 CharsSent:101 TokensEst:30}", agy)
	}
	oc := stats.AgentBreakdown["oc"]
	if oc.Count != 2 || oc.Prompts != 1 || oc.CharsSent != 100 || oc.TokensEst != 30 {
		t.Fatalf("oc breakdown = %+v, want {Count:2 Prompts:1 CharsSent:100 TokensEst:30}", oc)
	}

	details := robot.BuildAnalyticsSessionDetails(testEvents)
	if len(details) != 1 || details[0].AgentCount != 4 || details[0].PromptCount != 1 {
		t.Fatalf("BuildAnalyticsSessionDetails = %+v, want 1 session with AgentCount=4 PromptCount=1", details)
	}
}

func TestRobotAnalyticsFlagsRegistered(t *testing.T) {
	expected := []string{
		"robot-analytics",
		"analytics-days",
		"analytics-since",
		"analytics-sessions",
	}
	for _, flagName := range expected {
		if rootCmd.Flags().Lookup(flagName) == nil {
			t.Errorf("expected root flag --%s to be registered", flagName)
		}
	}
}

func TestRobotAnalyticsSubprocessAndJSONParity(t *testing.T) {
	t.Parallel()
	tmpRoot := t.TempDir()
	homeDir := filepath.Join(tmpRoot, "home")
	configHome := filepath.Join(tmpRoot, "config")
	projectDir := filepath.Join(tmpRoot, "project")
	emptyBin := filepath.Join(tmpRoot, "bin")
	eventsDir := filepath.Join(homeDir, ".config", "ntm", "analytics")
	for _, dir := range []string{homeDir, configHome, projectDir, emptyBin, eventsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	now := time.Now().UTC()
	fixtureEvents := []events.Event{
		{
			Timestamp: now.Add(-48 * time.Hour),
			Type:      events.EventSessionCreate,
			Session:   "alpha-swarm",
			Data: map[string]interface{}{
				"claude_count":      float64(1),
				"codex_count":       float64(1),
				"antigravity_count": float64(2),
				"opencode_count":    float64(1),
			},
		},
		{
			Timestamp: now.Add(-24 * time.Hour),
			Type:      events.EventAgentAdd,
			Session:   "alpha-swarm",
			Data:      map[string]interface{}{"agent_type": "agy"},
		},
		{
			Timestamp: now.Add(-12 * time.Hour),
			Type:      events.EventPromptSend,
			Session:   "alpha-swarm",
			Data: map[string]interface{}{
				"prompt_length":    float64(300),
				"estimated_tokens": float64(90),
				"target_types":     "cc,agy,oc",
			},
		},
		{
			Timestamp: now.Add(-6 * time.Hour),
			Type:      events.EventError,
			Session:   "alpha-swarm",
			Data:      map[string]interface{}{"error_type": "rate_limit"},
		},
		{
			Timestamp: now.Add(-3 * time.Hour),
			Type:      events.EventAgentCrash,
			Session:   "alpha-swarm",
		},
		{
			Timestamp: now.Add(-2 * time.Hour),
			Type:      events.EventAgentRestart,
			Session:   "alpha-swarm",
		},
		{
			Timestamp: now.AddDate(0, 0, -20),
			Type:      events.EventSessionCreate,
			Session:   "stale-outside-7d",
			Data:      map[string]interface{}{"claude_count": float64(5)},
		},
	}

	var rawLog bytes.Buffer
	for _, ev := range fixtureEvents {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		rawLog.Write(line)
		rawLog.WriteByte('\n')
	}
	logPath := filepath.Join(eventsDir, "events.jsonl")
	if err := os.WriteFile(logPath, rawLog.Bytes(), 0o600); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}

	runCLI := func(t *testing.T, args ...string) ([]byte, int, string) {
		t.Helper()
		rawArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
		cmd.Dir = projectDir
		cmd.Env = envWithOverrides(os.Environ(),
			"HOME="+homeDir,
			"XDG_CONFIG_HOME="+configHome,
			"PATH="+emptyBin,
			"NTM_NO_COLOR=1",
			"NTM_CONFIG=",
			"NTM_ROBOT_FORMAT=json",
			"NTM_OUTPUT_FORMAT=",
			"TOON_DEFAULT_FORMAT=",
			"NTM_ROBOT_VERBOSITY=",
			"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs),
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		exitCode := 0
		if runErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("run subprocess %v: %v (stderr=%s)", args, runErr, stderr.String())
			}
			exitCode = exitErr.ExitCode()
		}
		return stdout.Bytes(), exitCode, stderr.String()
	}

	robotBytes, robotExit, robotStderr := runCLI(t, "--robot-analytics", "--analytics-days=7", "--analytics-sessions", "--robot-format=json")
	if robotExit != 0 {
		t.Fatalf("--robot-analytics exit=%d want 0 (stdout=%s stderr=%s)", robotExit, string(robotBytes), robotStderr)
	}

	var robotOut robot.AnalyticsOutput
	if err := json.Unmarshal(robotBytes, &robotOut); err != nil {
		t.Fatalf("unmarshal --robot-analytics JSON: %v\nraw=%s", err, string(robotBytes))
	}
	if !robotOut.Success {
		t.Fatalf("expected robotOut.Success=true, got error=%q code=%q", robotOut.Error, robotOut.ErrorCode)
	}
	if robotOut.SchemaID != "ntm:robot:analytics:v1" || robotOut.SchemaVersion != "1.0.0" {
		t.Fatalf("unexpected schema metadata: id=%q version=%q", robotOut.SchemaID, robotOut.SchemaVersion)
	}
	if robotOut.TotalSessions != 1 || robotOut.TotalAgents != 6 || robotOut.TotalPrompts != 1 {
		t.Fatalf("unexpected robot totals: sessions=%d agents=%d prompts=%d", robotOut.TotalSessions, robotOut.TotalAgents, robotOut.TotalPrompts)
	}
	if robotOut.AgentCrashes != 1 || robotOut.AgentRestarts != 1 {
		t.Fatalf("unexpected crash/restart counts: crashes=%d restarts=%d", robotOut.AgentCrashes, robotOut.AgentRestarts)
	}
	if got := robotOut.AgentBreakdown["antigravity"]; got.Count != 3 || got.Prompts != 1 || got.CharsSent != 100 || got.TokensEst != 30 {
		t.Fatalf("robot antigravity breakdown = %+v, want {Count:3 Prompts:1 CharsSent:100 TokensEst:30}", got)
	}
	if got := robotOut.AgentBreakdown["oc"]; got.Count != 1 || got.Prompts != 1 || got.CharsSent != 100 || got.TokensEst != 30 {
		t.Fatalf("robot oc breakdown = %+v, want {Count:1 Prompts:1 CharsSent:100 TokensEst:30}", got)
	}
	if robotOut.ErrorCount != 1 || robotOut.ErrorTypes["rate_limit"] != 1 {
		t.Fatalf("robot error stats = count:%d types:%v", robotOut.ErrorCount, robotOut.ErrorTypes)
	}
	if len(robotOut.Sessions) != 1 || robotOut.Sessions[0].Name != "alpha-swarm" || robotOut.Sessions[0].AgentCount != 6 || robotOut.Sessions[0].PromptCount != 1 {
		t.Fatalf("robot sessions = %+v, want [alpha-swarm AgentCount=6 PromptCount=1]", robotOut.Sessions)
	}

	cliBytes, cliExit, cliStderr := runCLI(t, "analytics", "--days", "7", "--sessions", "--json")
	if cliExit != 0 {
		t.Fatalf("ntm analytics --json exit=%d want 0 (stdout=%s stderr=%s)", cliExit, string(cliBytes), cliStderr)
	}

	var cliOut output.AnalyticsResponse
	if err := json.Unmarshal(cliBytes, &cliOut); err != nil {
		t.Fatalf("unmarshal ntm analytics --json: %v\nraw=%s", err, string(cliBytes))
	}
	if cliOut.TotalSessions != robotOut.TotalSessions ||
		cliOut.TotalAgents != robotOut.TotalAgents ||
		cliOut.TotalPrompts != robotOut.TotalPrompts ||
		cliOut.TotalCharsSent != robotOut.TotalCharsSent ||
		cliOut.TotalTokensEst != robotOut.TotalTokensEst ||
		cliOut.ErrorCount != robotOut.ErrorCount ||
		cliOut.AgentCrashes != robotOut.AgentCrashes ||
		cliOut.AgentRestarts != robotOut.AgentRestarts {
		t.Fatalf("ntm analytics --json totals diverged from --robot-analytics:\ncli=%+v\nrobot=%+v", cliOut, robotOut)
	}
	if len(cliOut.AgentBreakdown) != len(robotOut.AgentBreakdown) {
		t.Fatalf("cli agent_breakdown len=%d want %d (%v)", len(cliOut.AgentBreakdown), len(robotOut.AgentBreakdown), cliOut.AgentBreakdown)
	}
	for k, want := range robotOut.AgentBreakdown {
		if got, ok := cliOut.AgentBreakdown[k]; !ok || got != want {
			t.Fatalf("cli agent_breakdown[%q] = %+v, want %+v", k, got, want)
		}
	}
	if cliOut.ErrorTypes["rate_limit"] != 1 {
		t.Fatalf("cli error_types lost rate_limit: %v", cliOut.ErrorTypes)
	}
	if len(cliOut.Sessions) != 1 || cliOut.Sessions[0].Name != "alpha-swarm" || cliOut.Sessions[0].AgentCount != 6 {
		t.Fatalf("cli --sessions --json lost sessions: %+v", cliOut.Sessions)
	}

	promBytes, promExit, promStderr := runCLI(t, "analytics", "--days", "7", "--format", "prometheus")
	if promExit != 0 {
		t.Fatalf("ntm analytics --format prometheus exit=%d (stderr=%s)", promExit, promStderr)
	}
	promStr := string(promBytes)
	for _, needle := range []string{
		`ntm_agent_crashes_total{period="Last 7 days"} 1`,
		`ntm_agent_restarts_total{period="Last 7 days"} 1`,
		`ntm_agent_spawns{period="Last 7 days",agent_type="antigravity"} 3`,
		`ntm_agent_spawns{period="Last 7 days",agent_type="oc"} 1`,
		`ntm_errors_by_type{period="Last 7 days",error_type="rate_limit"} 1`,
	} {
		if !strings.Contains(promStr, needle) {
			t.Errorf("prometheus output missing %q:\n%s", needle, promStr)
		}
	}

	errBytes, errExit, _ := runCLI(t, "--robot-analytics", "--analytics-since=not-a-date", "--robot-format=json")
	if errExit != 1 {
		t.Fatalf("expected invalid --analytics-since to exit 1, got %d (stdout=%s)", errExit, string(errBytes))
	}
	var errOut robot.AnalyticsOutput
	if err := json.Unmarshal(errBytes, &errOut); err != nil {
		t.Fatalf("unmarshal invalid --analytics-since output: %v\nraw=%s", err, string(errBytes))
	}
	if errOut.Success || errOut.ErrorCode != robot.ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG error envelope, got %+v", errOut.RobotResponse)
	}
}
