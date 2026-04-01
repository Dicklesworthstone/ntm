package panels

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ---------------------------------------------------------------------------
// CostTrend.Arrow — 50% → 100%
// ---------------------------------------------------------------------------

func TestCostTrend_Arrow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		trend CostTrend
		want  string
	}{
		{"up", CostTrendUp, "↑"},
		{"down", CostTrendDown, "↓"},
		{"flat", CostTrendFlat, "→"},
		{"unknown", CostTrend(99), "→"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.trend.Arrow(); got != tt.want {
				t.Errorf("Arrow() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatTokenShort — 57.1% → 100%
// ---------------------------------------------------------------------------

func TestFormatTokenShort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tokens int
		want   string
	}{
		{"zero", 0, "0"},
		{"negative", -5, "0"},
		{"small", 999, "999"},
		{"1K", 1000, "1.0K"},
		{"middle_K", 45000, "45.0K"},
		{"999K", 999999, "1000.0K"},
		{"1M", 1000000, "1.0M"},
		{"large", 5500000, "5.5M"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatTokenShort(tt.tokens); got != tt.want {
				t.Errorf("formatTokenShort(%d) = %q, want %q", tt.tokens, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// padRight/padLeft — 60%/80% → 100%
// ---------------------------------------------------------------------------

func TestPadRight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"zero_width", "hello", 0, ""},
		{"negative_width", "hello", -5, ""},
		{"no_padding", "hello", 5, "hello"},
		{"longer", "hello", 3, "hello"},
		{"pad", "hi", 5, "hi   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := padRight(tt.s, tt.width); got != tt.want {
				t.Errorf("padRight(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
		})
	}
}

func TestPadLeft(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"zero_width", "hello", 0, ""},
		{"negative_width", "hello", -5, ""},
		{"no_padding", "hello", 5, "hello"},
		{"longer", "hello", 3, "hello"},
		{"pad", "hi", 5, "   hi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := padLeft(tt.s, tt.width); got != tt.want {
				t.Errorf("padLeft(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatTimelineSpan — 61.5% → 100%
// ---------------------------------------------------------------------------

func TestFormatTimelineSpan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "0s"},
		{"negative", -10 * time.Second, "0s"},
		{"seconds", 45 * time.Second, "45s"},
		{"minutes_only", 5 * time.Minute, "5m"},
		{"minutes_seconds", 5*time.Minute + 30*time.Second, "5m"}, // rounds to nearest second, only shows minutes
		{"hours_only", 2 * time.Hour, "2h"},
		{"hours_minutes", 2*time.Hour + 30*time.Minute, "2h30m"},
		{"hours_no_minutes", 3*time.Hour + 0*time.Minute, "3h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatTimelineSpan(tt.d); got != tt.want {
				t.Errorf("formatTimelineSpan(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseAgentTypePrefix — 0% → 100%
// ---------------------------------------------------------------------------

func TestParseAgentTypePrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		wantT  tmux.AgentType
		wantOk bool
	}{
		{"empty", "", "", false},
		{"no_underscore", "claude", "", false},
		{"valid_cc", "cc_1", tmux.AgentClaude, true},
		{"valid_cod", "cod_2", tmux.AgentCodex, true},
		{"valid_gmi", "gmi_3", tmux.AgentGemini, true},
		{"invalid_type", "foo_1", "", false},
		{"user_type", "user_1", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotT, gotOk := parseAgentTypePrefix(tt.target)
			if gotOk != tt.wantOk {
				t.Errorf("parseAgentTypePrefix(%q) ok = %v, want %v", tt.target, gotOk, tt.wantOk)
			}
			if gotOk && gotT != tt.wantT {
				t.Errorf("parseAgentTypePrefix(%q) type = %q, want %q", tt.target, gotT, tt.wantT)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatHistoryPaneLabel — 28.6% → 100%
// ---------------------------------------------------------------------------

func TestFormatHistoryPaneLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pane tmux.Pane
		want string
	}{
		{
			name: "ntm_index_with_type",
			pane: tmux.Pane{NTMIndex: 2, Type: tmux.AgentClaude},
			want: "cc_2",
		},
		{
			name: "ntm_index_unknown_type",
			pane: tmux.Pane{NTMIndex: 1, Type: tmux.AgentUnknown},
			want: "0", // Falls through to Index
		},
		{
			name: "ntm_index_user_type",
			pane: tmux.Pane{NTMIndex: 1, Type: tmux.AgentUser},
			want: "0", // Falls through to Index
		},
		{
			name: "title_with_double_underscore",
			pane: tmux.Pane{Title: "prefix__suffix"},
			want: "suffix",
		},
		{
			name: "title_without_double_underscore",
			pane: tmux.Pane{Title: "simple_title"},
			want: "simple_title",
		},
		{
			name: "fallback_to_index",
			pane: tmux.Pane{Index: 5},
			want: "5",
		},
		{
			name: "embedded_double_underscore_session",
			pane: tmux.Pane{Title: "my__project__cod_2"},
			want: "cod_2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatHistoryPaneLabel(tt.pane); got != tt.want {
				t.Errorf("formatHistoryPaneLabel(%+v) = %q, want %q", tt.pane, got, tt.want)
			}
		})
	}
}
