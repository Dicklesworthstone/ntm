package robot

import (
	"encoding/json"
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// parseStringSlice — 66.7% → 100%
// ---------------------------------------------------------------------------

func TestParseStringSlice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value interface{}
		want  []string
	}{
		{"nil", nil, []string{}},
		{"empty string", "", []string{}},
		{"single string", "hello", []string{"hello"}},
		{"slice of interfaces", []interface{}{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"slice with empty", []interface{}{"a", "", "c"}, []string{"a", "c"}},
		{"slice with nil", []interface{}{"a", nil, "c"}, []string{"a", "c"}},
		{"int falls through", 42, []string{}},
		{"bool falls through", true, []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseStringSlice(tt.value)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseStringSlice(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// appendUniqueStrings — 76.9% → 100%
// ---------------------------------------------------------------------------

func TestAppendUniqueStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dst  []string
		src  []string
		want []string
	}{
		{"empty to empty", []string{}, []string{}, []string{}},
		{"add to empty", []string{}, []string{"a", "b"}, []string{"a", "b"}},
		{"add unique", []string{"a"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{"skip duplicates", []string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{"skip empty in src", []string{"a"}, []string{"", "b"}, []string{"a", "b"}},
		{"skip empty in dst", []string{"", "a"}, []string{"b"}, []string{"", "a", "b"}},
		{"all duplicates", []string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := appendUniqueStrings(tt.dst, tt.src...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("appendUniqueStrings(%v, %v...) = %v, want %v", tt.dst, tt.src, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// stringValue — 75% → 100%
// ---------------------------------------------------------------------------

func TestStringValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value interface{}
		want  string
	}{
		{"nil", nil, ""},
		{"string", "hello", "hello"},
		{"int", 42, "42"},
		{"bool", true, "true"},
		{"float", 3.14, "3.14"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stringValue(tt.value)
			if got != tt.want {
				t.Errorf("stringValue(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// firstNonEmpty — 75% → 100%
// ---------------------------------------------------------------------------

func TestFirstNonEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{"all empty", []string{}, ""},
		{"first non-empty", []string{"", "hello", "world"}, "hello"},
		{"first is non-empty", []string{"hello", "world"}, "hello"},
		{"all whitespace", []string{"", "  ", "\t"}, ""},
		{"whitespace then value", []string{"  ", "hello"}, "hello"},
		{"single value", []string{"hello"}, "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := firstNonEmpty(tt.values...)
			if got != tt.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tt.values, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// countJSONArray — 66.7% → 100%
// ---------------------------------------------------------------------------

func TestCountJSONArray(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  json.RawMessage
		want int
	}{
		{"empty", json.RawMessage{}, 0},
		{"null", json.RawMessage("null"), 0},
		{"empty array", json.RawMessage("[]"), 0},
		{"one item", json.RawMessage(`["a"]`), 1},
		{"three items", json.RawMessage(`["a","b","c"]`), 3},
		{"invalid json", json.RawMessage("not-json"), 0},
		{"object not array", json.RawMessage(`{"a":1}`), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := countJSONArray(tt.raw)
			if got != tt.want {
				t.Errorf("countJSONArray(%s) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// agentTypeFromProgram — 44.4% → 100%
// ---------------------------------------------------------------------------

func TestAgentTypeFromProgram(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		program string
		want    string
	}{
		{"claude", "claude-code", "cc"},
		{"claude uppercase", "Claude Code", "cc"},
		{"codex", "codex-cli", "cod"},
		{"gemini", "gemini-pro", "gmi"},
		{"cursor", "cursor-ai", "cursor"},
		{"windsurf", "Windsurf IDE", "windsurf"},
		{"aider", "aider-chat", "aider"},
		{"unknown", "unknown-agent", "unknown-agent"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := agentTypeFromProgram(tt.program)
			if got != tt.want {
				t.Errorf("agentTypeFromProgram(%q) = %q, want %q", tt.program, got, tt.want)
			}
		})
	}
}
