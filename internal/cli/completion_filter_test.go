package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ---------------------------------------------------------------------------
// filterByPrefix — 28.6% → 100%
// ---------------------------------------------------------------------------

func TestCompletionAgentID(t *testing.T) {

	tests := []struct {
		name string
		pane tmux.Pane
		want string
		ok   bool
	}{
		{name: "canonical claude", pane: tmux.Pane{Type: tmux.AgentClaude, NTMIndex: 1}, want: "cc_1", ok: true},
		{name: "aliased codex", pane: tmux.Pane{Type: tmux.AgentType("openai-codex"), NTMIndex: 2}, want: "cod_2", ok: true},
		{name: "aliased windsurf", pane: tmux.Pane{Type: tmux.AgentType("ws"), NTMIndex: 3}, want: "windsurf_3", ok: true},
		{name: "ollama included", pane: tmux.Pane{Type: tmux.AgentOllama, NTMIndex: 4}, want: "ollama_4", ok: true},
		{name: "controller ignored", pane: tmux.Pane{Type: tmux.AgentType("controller_codex"), NTMIndex: 1}, want: "", ok: false},
		{name: "no ntm index ignored", pane: tmux.Pane{Type: tmux.AgentClaude, NTMIndex: 0}, want: "", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := completionAgentID(tc.pane)
			if ok != tc.ok {
				t.Fatalf("completionAgentID(%+v) ok = %v, want %v", tc.pane, ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("completionAgentID(%+v) = %q, want %q", tc.pane, got, tc.want)
			}
		})
	}
}

func TestFilterByPrefix(t *testing.T) {

	tests := []struct {
		name    string
		options []string
		prefix  string
		wantLen int
	}{
		{"empty prefix returns all", []string{"a", "b", "c"}, "", 3},
		{"matching prefix", []string{"foo", "foobar", "baz"}, "foo", 2},
		{"no matches", []string{"abc", "def"}, "xyz", 0},
		{"empty options", []string{}, "foo", 0},
		{"nil options", nil, "foo", 0},
		{"exact match", []string{"hello"}, "hello", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterByPrefix(tc.options, tc.prefix)
			if len(got) != tc.wantLen {
				t.Errorf("filterByPrefix(%v, %q) returned %d items, want %d", tc.options, tc.prefix, len(got), tc.wantLen)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// coerceDepsResponse — 33.3% → 100%
// ---------------------------------------------------------------------------

func TestCoerceDepsResponse(t *testing.T) {

	t.Run("direct value", func(t *testing.T) {
		resp := output.DepsResponse{AllInstalled: true}
		got, err := coerceDepsResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.AllInstalled {
			t.Error("expected AllInstalled=true")
		}
	})

	t.Run("pointer value", func(t *testing.T) {
		resp := &output.DepsResponse{AllInstalled: true}
		got, err := coerceDepsResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.AllInstalled {
			t.Error("expected AllInstalled=true")
		}
	})

	t.Run("nil pointer", func(t *testing.T) {
		var resp *output.DepsResponse
		_, err := coerceDepsResponse(resp)
		if err == nil {
			t.Fatal("expected error for nil pointer")
		}
	})

	t.Run("unexpected type", func(t *testing.T) {
		_, err := coerceDepsResponse("not a DepsResponse")
		if err == nil {
			t.Fatal("expected error for wrong type")
		}
	})
}

// ---------------------------------------------------------------------------
// agentTypeToString — 85.7% → 100%
// ---------------------------------------------------------------------------

func TestAgentTypeToString(t *testing.T) {

	tests := []struct {
		name string
		in   tmux.AgentType
		want string
	}{
		{"claude", tmux.AgentClaude, "claude"},
		{"codex", tmux.AgentCodex, "codex"},
		{"gemini", tmux.AgentGemini, "gemini"},
		{"claude alias", tmux.AgentType("claude_code"), "claude"},
		{"codex alias", tmux.AgentType("openai-codex"), "codex"},
		{"gemini alias", tmux.AgentType("google-gemini"), "gemini"},
		{"windsurf alias", tmux.AgentType("ws"), "windsurf"},
		{"empty string type", tmux.AgentType(""), "unknown"},
		{"custom type", tmux.AgentType("aider"), "aider"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := agentTypeToString(tc.in)
			if got != tc.want {
				t.Errorf("agentTypeToString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
