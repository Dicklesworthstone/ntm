package cli

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestStateIcon(t *testing.T) {
	tests := []struct {
		state string
		want  string
	}{
		{"WAITING", "●"},
		{"GENERATING", "▶"},
		{"THINKING", "◐"},
		{"ERROR", "✗"},
		{"STALLED", "◯"},
		{"unknown", "?"},
		{"", "?"},
		{"waiting", "?"}, // case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			got := stateIcon(tt.state)
			if got != tt.want {
				t.Errorf("stateIcon(%q) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestFormatActivityDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"zero", 0, "-"},
		{"1 second", 1 * time.Second, "1s"},
		{"30 seconds", 30 * time.Second, "30s"},
		{"59 seconds", 59 * time.Second, "59s"},
		{"1 minute", 1 * time.Minute, "1m0s"},
		{"1 minute 30 seconds", 90 * time.Second, "1m30s"},
		{"5 minutes", 5 * time.Minute, "5m0s"},
		{"5 minutes 45 seconds", 5*time.Minute + 45*time.Second, "5m45s"},
		{"59 minutes 59 seconds", 59*time.Minute + 59*time.Second, "59m59s"},
		{"1 hour", 1 * time.Hour, "1h0m"},
		{"1 hour 30 minutes", 90 * time.Minute, "1h30m"},
		{"2 hours 15 minutes", 2*time.Hour + 15*time.Minute, "2h15m"},
		{"24 hours", 24 * time.Hour, "24h0m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatActivityDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatActivityDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestPassesFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType string
		pane      tmux.Pane
		opts      activityOptions
		want      bool
	}{
		{
			name:      "no_filters_allows_all",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{},
			want:      true,
		},
		{
			name:      "filter_by_pane_title_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterPane: "cc_1"},
			want:      true,
		},
		{
			name:      "filter_by_pane_title_no_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterPane: "cc_2"},
			want:      false,
		},
		{
			name:      "filter_by_pane_index_match",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterPane: "2"},
			want:      true,
		},
		{
			name:      "filter_by_pane_index_no_match",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterPane: "3"},
			want:      false,
		},
		{
			name:      "filter_claude_type_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterClaude: true},
			want:      true,
		},
		{
			name:      "filter_claude_type_no_match",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterClaude: true},
			want:      false,
		},
		{
			name:      "filter_codex_type_match",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterCodex: true},
			want:      true,
		},
		{
			name:      "filter_codex_type_no_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterCodex: true},
			want:      false,
		},
		{
			name:      "filter_gemini_type_match",
			agentType: "gemini",
			pane:      tmux.Pane{Index: 3, Title: "gmi_3"},
			opts:      activityOptions{filterGemini: true},
			want:      true,
		},
		{
			name:      "filter_gemini_type_no_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterGemini: true},
			want:      false,
		},
		{
			name:      "multiple_type_filters_match_first",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterClaude: true, filterCodex: true},
			want:      true,
		},
		{
			name:      "multiple_type_filters_match_second",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterClaude: true, filterCodex: true},
			want:      true,
		},
		{
			name:      "multiple_type_filters_no_match",
			agentType: "gemini",
			pane:      tmux.Pane{Index: 3, Title: "gmi_3"},
			opts:      activityOptions{filterClaude: true, filterCodex: true},
			want:      false,
		},
		{
			name:      "all_type_filters_match_all",
			agentType: "gemini",
			pane:      tmux.Pane{Index: 3, Title: "gmi_3"},
			opts:      activityOptions{filterClaude: true, filterCodex: true, filterGemini: true},
			want:      true,
		},
		{
			name:      "pane_filter_takes_precedence_over_type",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterPane: "cc_1", filterCodex: true},
			want:      true, // pane filter matches, type filter is ignored
		},
		{
			name:      "pane_filter_precedence_no_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterPane: "cc_99", filterClaude: true},
			want:      false, // pane filter doesn't match, type filter is ignored
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := passesFilter(tt.agentType, tt.pane, tt.opts)
			if got != tt.want {
				t.Errorf("passesFilter(%q, %v, %+v) = %v, want %v",
					tt.agentType, tt.pane, tt.opts, got, tt.want)
			}
		})
	}
}

func TestActivityAgentTypeColor(t *testing.T) {
	t.Parallel()

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
		{"windsurf alias", "ws", string(current.Windsurf)},
		{"aider", "aider", string(current.Aider)},
		{"ollama", "ollama", string(current.Ollama)},
		{"user", "user", string(current.User)},
		{"unknown", "mystery", string(current.Text)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(activityAgentTypeColor(tc.agentType, current)); got != tc.want {
				t.Fatalf("activityAgentTypeColor(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}
