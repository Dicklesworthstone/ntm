package robot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
)

// =============================================================================
// splitJSONLines tests
// =============================================================================

func TestSplitJSONLines(t *testing.T) {

	tests := []struct {
		name     string
		input    []byte
		wantLen  int
		wantVals []string
	}{
		{
			name:     "empty input",
			input:    []byte{},
			wantLen:  0,
			wantVals: nil,
		},
		{
			name:     "single line no newline",
			input:    []byte(`{"a":1}`),
			wantLen:  1,
			wantVals: []string{`{"a":1}`},
		},
		{
			name:     "single line with newline",
			input:    []byte("{\"a\":1}\n"),
			wantLen:  1,
			wantVals: []string{`{"a":1}`},
		},
		{
			name:     "two lines",
			input:    []byte("{\"a\":1}\n{\"b\":2}\n"),
			wantLen:  2,
			wantVals: []string{`{"a":1}`, `{"b":2}`},
		},
		{
			name:     "trailing data after last newline",
			input:    []byte("{\"a\":1}\n{\"b\":2}"),
			wantLen:  2,
			wantVals: []string{`{"a":1}`, `{"b":2}`},
		},
		{
			name:     "empty lines between data",
			input:    []byte("{\"a\":1}\n\n{\"b\":2}\n"),
			wantLen:  3,
			wantVals: []string{`{"a":1}`, ``, `{"b":2}`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitJSONLines(tc.input)
			if len(got) != tc.wantLen {
				t.Errorf("splitJSONLines() returned %d lines, want %d", len(got), tc.wantLen)
			}
			for i, want := range tc.wantVals {
				if i >= len(got) {
					break
				}
				if string(got[i]) != want {
					t.Errorf("line %d = %q, want %q", i, string(got[i]), want)
				}
			}
		})
	}
}

// =============================================================================
// aggregateTokenStats tests
// =============================================================================

func TestAggregateTokenStats(t *testing.T) {

	t.Run("empty events", func(t *testing.T) {
		output := aggregateTokenStats(nil, 7, "", "agent")
		if output.TotalTokens != 0 {
			t.Errorf("expected 0 total tokens, got %d", output.TotalTokens)
		}
		if output.TotalPrompts != 0 {
			t.Errorf("expected 0 total prompts, got %d", output.TotalPrompts)
		}
		if len(output.Breakdown) != 0 {
			t.Errorf("expected empty breakdown, got %d entries", len(output.Breakdown))
		}
	})

	t.Run("prompt events aggregate correctly", func(t *testing.T) {
		eventList := []events.Event{
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(100),
					"prompt_length":    float64(350),
					"target_types":     "cc",
				},
			},
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(200),
					"prompt_length":    float64(700),
					"target_types":     "cod",
				},
			},
		}

		output := aggregateTokenStats(eventList, 7, "", "agent")
		if output.TotalTokens != 300 {
			t.Errorf("expected 300 total tokens, got %d", output.TotalTokens)
		}
		if output.TotalPrompts != 2 {
			t.Errorf("expected 2 total prompts, got %d", output.TotalPrompts)
		}
		if output.TotalCharacters != 1050 {
			t.Errorf("expected 1050 total chars, got %d", output.TotalCharacters)
		}
	})

	t.Run("broadcast prompt retains token remainder and one model prompt", func(t *testing.T) {
		eventList := []events.Event{{
			Type:      events.EventPromptSend,
			Timestamp: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
			Data: map[string]interface{}{
				"estimated_tokens": float64(10),
				"prompt_length":    float64(10),
				"target_types":     "cc,cod,gmi",
				"model":            "shared-model",
			},
		}}

		output := aggregateTokenStats(eventList, 7, "", "model")
		if output.TotalTokens != 10 || output.TotalCharacters != 10 || output.TotalPrompts != 1 {
			t.Fatalf("totals = %+v", output)
		}
		if got := output.AgentStats["claude"].Tokens + output.AgentStats["codex"].Tokens + output.AgentStats["gemini"].Tokens; got != output.TotalTokens {
			t.Fatalf("agent token total = %d, want %d", got, output.TotalTokens)
		}
		if got := output.ModelStats["shared-model"]; got.Tokens != 10 || got.Prompts != 1 || got.Characters != 10 {
			t.Fatalf("model stats = %+v", got)
		}
	})

	t.Run("session create tracks spawns with token usage", func(t *testing.T) {
		// AgentStats are only populated for agents with token usage,
		// so we need both session create and prompt events.
		eventList := []events.Event{
			{
				Type:      events.EventSessionCreate,
				Timestamp: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"claude_count": float64(3),
					"codex_count":  float64(2),
					"gemini_count": float64(1),
					"grok_count":   float64(5),
					"ollama_count": float64(4),
				},
			},
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 10, 5, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(100),
					"prompt_length":    float64(350),
					"target_types":     "cc",
				},
			},
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 10, 10, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(50),
					"prompt_length":    float64(175),
					"target_types":     "cod",
				},
			},
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 10, 15, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(75),
					"prompt_length":    float64(250),
					"target_types":     "ollama",
				},
			},
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 10, 20, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(125),
					"prompt_length":    float64(438),
					"target_types":     "xai_grok_build",
				},
			},
		}

		output := aggregateTokenStats(eventList, 7, "", "agent")
		if output.AgentStats["claude"].Spawned != 3 {
			t.Errorf("expected 3 claude spawns, got %d", output.AgentStats["claude"].Spawned)
		}
		if output.AgentStats["codex"].Spawned != 2 {
			t.Errorf("expected 2 codex spawns, got %d", output.AgentStats["codex"].Spawned)
		}
		if output.AgentStats["grok"].Spawned != 5 {
			t.Errorf("expected 5 grok spawns, got %d", output.AgentStats["grok"].Spawned)
		}
		if output.AgentStats["ollama"].Spawned != 4 {
			t.Errorf("expected 4 ollama spawns, got %d", output.AgentStats["ollama"].Spawned)
		}
	})

	t.Run("group by day", func(t *testing.T) {
		eventList := []events.Event{
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(100),
					"prompt_length":    float64(350),
					"target_types":     "cc",
				},
			},
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 16, 10, 0, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(200),
					"prompt_length":    float64(700),
					"target_types":     "cc",
				},
			},
		}

		output := aggregateTokenStats(eventList, 7, "", "day")
		if len(output.TimeStats) != 2 {
			t.Errorf("expected 2 time stats, got %d", len(output.TimeStats))
		}
		if len(output.Breakdown) != 2 {
			t.Errorf("expected 2 breakdown entries for day grouping, got %d", len(output.Breakdown))
		}
	})

	t.Run("group by model", func(t *testing.T) {
		eventList := []events.Event{
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(100),
					"prompt_length":    float64(350),
					"target_types":     "cc",
					"model":            "opus-4.5",
				},
			},
		}

		output := aggregateTokenStats(eventList, 7, "", "model")
		if _, ok := output.ModelStats["opus-4.5"]; !ok {
			t.Error("expected model stats for opus-4.5")
		}
		if len(output.Breakdown) != 1 {
			t.Errorf("expected 1 breakdown entry for model grouping, got %d", len(output.Breakdown))
		}
	})

	t.Run("breakdown sorted by tokens descending", func(t *testing.T) {
		eventList := []events.Event{
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(50),
					"prompt_length":    float64(175),
					"target_types":     "gmi",
				},
			},
			{
				Type:      events.EventPromptSend,
				Timestamp: time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC),
				Data: map[string]interface{}{
					"estimated_tokens": float64(200),
					"prompt_length":    float64(700),
					"target_types":     "cc",
				},
			},
		}

		output := aggregateTokenStats(eventList, 7, "", "agent")
		if len(output.Breakdown) < 2 {
			t.Fatalf("expected >= 2 breakdown entries, got %d", len(output.Breakdown))
		}
		if output.Breakdown[0].Tokens < output.Breakdown[1].Tokens {
			t.Errorf("breakdown not sorted descending: %d < %d",
				output.Breakdown[0].Tokens, output.Breakdown[1].Tokens)
		}
	})
}

func TestParseAgentTypes_GrokAndLegacyAll(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{input: "grok", want: []string{"grok"}},
		{input: "grok-build", want: []string{"grok"}},
		{input: "xai_grok_build", want: []string{"grok"}},
		{input: "all", want: []string{"claude", "codex", "gemini", "grok"}},
	}

	for _, tt := range tests {
		got := parseAgentTypes(tt.input)
		if len(got) != len(tt.want) {
			t.Fatalf("parseAgentTypes(%q) = %v, want %v", tt.input, got, tt.want)
		}
		for i, want := range tt.want {
			if got[i] != want {
				t.Fatalf("parseAgentTypes(%q)[%d] = %q, want %q", tt.input, i, got[i], want)
			}
		}
	}
}

func TestReadTokenEvents_NormalizesGrokFilter(t *testing.T) {
	now := time.Now().UTC()
	eventList := []events.Event{
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{"target_types": "grok"}},
		{Timestamp: now, Type: events.EventPromptSend, Session: "s1", Data: map[string]interface{}{"target_types": "cod"}},
	}

	var data []byte
	for _, event := range eventList {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	got, err := readTokenEvents(path, now.Add(-time.Hour), "", "xai-grok-build")
	if err != nil {
		t.Fatalf("readTokenEvents() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("readTokenEvents() returned %d events, want 1", len(got))
	}
	if targets, _ := got[0].Data["target_types"].(string); targets != "grok" {
		t.Fatalf("matched target_types = %q, want grok", targets)
	}
}
