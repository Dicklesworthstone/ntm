package robot

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
)

// =============================================================================
// splitJSONLines tests
// =============================================================================

func TestSplitJSONLines(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
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
	t.Parallel()

	t.Run("empty events", func(t *testing.T) {
		t.Parallel()
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
		t.Parallel()
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

	t.Run("session create tracks spawns with token usage", func(t *testing.T) {
		t.Parallel()
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
		}

		output := aggregateTokenStats(eventList, 7, "", "agent")
		if output.AgentStats["claude"].Spawned != 3 {
			t.Errorf("expected 3 claude spawns, got %d", output.AgentStats["claude"].Spawned)
		}
		if output.AgentStats["codex"].Spawned != 2 {
			t.Errorf("expected 2 codex spawns, got %d", output.AgentStats["codex"].Spawned)
		}
		if output.AgentStats["ollama"].Spawned != 4 {
			t.Errorf("expected 4 ollama spawns, got %d", output.AgentStats["ollama"].Spawned)
		}
	})

	t.Run("group by day", func(t *testing.T) {
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
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
