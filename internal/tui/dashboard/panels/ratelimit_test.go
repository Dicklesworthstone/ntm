package panels

import (
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestMin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b int
		want int
	}{
		{"a smaller", 1, 5, 1},
		{"b smaller", 10, 3, 3},
		{"equal", 7, 7, 7},
		{"zero and positive", 0, 5, 0},
		{"negative values", -3, -7, -7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := min(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("min(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestRateLimitPanel_FormatLastActivity(t *testing.T) {
	t.Parallel()

	p := NewRateLimitPanel()

	tests := []struct {
		name string
		sec  int
		want string
	}{
		{"negative", -1, "?"},
		{"zero seconds", 0, "0s"},
		{"30 seconds", 30, "30s"},
		{"59 seconds", 59, "59s"},
		{"60 seconds", 60, "1m"},
		{"90 seconds", 90, "1m"},
		{"5 minutes", 300, "5m"},
		{"59 minutes", 3540, "59m"},
		{"1 hour", 3600, "1h"},
		{"2 hours", 7200, "2h"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := p.formatLastActivity(tc.sec)
			if got != tc.want {
				t.Errorf("formatLastActivity(%d) = %q, want %q", tc.sec, got, tc.want)
			}
		})
	}
}

func TestRateLimitPanel_ShortAgentType(t *testing.T) {
	t.Parallel()

	p := NewRateLimitPanel()

	tests := []struct {
		name      string
		agentType string
		want      string
	}{
		{"claude", "claude", "cc"},
		{"claude alias", "claude_code", "cc"},
		{"codex", "codex", "cod"},
		{"codex alias", "openai-codex", "cod"},
		{"gemini", "gemini", "gmi"},
		{"gemini alias", "google-gemini", "gmi"},
		{"short unknown", "ab", "ab"},
		{"medium unknown", "custom", "cus"},
		{"long unknown", "verylongname", "ver"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := p.shortAgentType(tc.agentType)
			if got != tc.want {
				t.Errorf("shortAgentType(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestRateLimitPanel_GetOAuthIcon(t *testing.T) {
	t.Parallel()

	p := NewRateLimitPanel()

	tests := []struct {
		name   string
		status robot.OAuthStatus
		want   string
	}{
		{"valid", robot.OAuthValid, "ok"},
		{"expired", robot.OAuthExpired, "exp"},
		{"error", robot.OAuthError, "err"},
		{"unknown", robot.OAuthStatus("unknown"), "?"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := p.getOAuthIcon(tc.status)
			if got != tc.want {
				t.Errorf("getOAuthIcon(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

func TestRateLimitPanel_GetRateLimitIcon(t *testing.T) {
	t.Parallel()

	p := NewRateLimitPanel()

	tests := []struct {
		name   string
		status robot.RateLimitStatus
		want   string
	}{
		{"ok", robot.RateLimitOK, "OK"},
		{"warning", robot.RateLimitWarning, "WARN"},
		{"limited", robot.RateLimitLimited, "LIM"},
		{"unknown", robot.RateLimitStatus("unknown"), "?"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := p.getRateLimitIcon(tc.status)
			if got != tc.want {
				t.Errorf("getRateLimitIcon(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

func TestRateLimitConfig(t *testing.T) {
	t.Parallel()

	cfg := rateLimitConfig()

	if cfg.ID != "ratelimit" {
		t.Errorf("ID = %q, want %q", cfg.ID, "ratelimit")
	}
	if cfg.Title != "OAuth/Rate Status" {
		t.Errorf("Title = %q, want %q", cfg.Title, "OAuth/Rate Status")
	}
	if cfg.Priority != PriorityHigh {
		t.Errorf("Priority = %v, want %v", cfg.Priority, PriorityHigh)
	}
	if cfg.RefreshInterval != 5*time.Second {
		t.Errorf("RefreshInterval = %v, want %v", cfg.RefreshInterval, 5*time.Second)
	}
	if cfg.MinWidth != 30 {
		t.Errorf("MinWidth = %d, want %d", cfg.MinWidth, 30)
	}
	if cfg.MinHeight != 6 {
		t.Errorf("MinHeight = %d, want %d", cfg.MinHeight, 6)
	}
	if !cfg.Collapsible {
		t.Error("Collapsible should be true")
	}
}

func TestNewRateLimitPanel(t *testing.T) {
	t.Parallel()

	p := NewRateLimitPanel()

	if p == nil {
		t.Fatal("NewRateLimitPanel returned nil")
	}
	if p.agents != nil {
		t.Error("new panel should have nil agents")
	}
	if p.err != nil {
		t.Error("new panel should have nil error")
	}

	cfg := p.Config()
	if cfg.ID != "ratelimit" {
		t.Errorf("Config ID = %q, want %q", cfg.ID, "ratelimit")
	}
}

func TestRateLimitPanel_SetData(t *testing.T) {
	t.Parallel()

	p := NewRateLimitPanel()

	agents := []robot.AgentOAuthHealth{
		{AgentType: "claude", Pane: 1, OAuthStatus: robot.OAuthValid},
		{AgentType: "codex", Pane: 2, OAuthStatus: robot.OAuthExpired},
	}

	p.SetData(agents, nil)

	if len(p.agents) != 2 {
		t.Errorf("agents count = %d, want 2", len(p.agents))
	}
	if p.err != nil {
		t.Error("err should be nil")
	}
}

func TestRateLimitPanel_FormatAgentRow_TruncatesSafelyForNarrowWidth(t *testing.T) {
	t.Parallel()

	p := NewRateLimitPanel()
	agent := robot.AgentOAuthHealth{
		AgentType:         "claude",
		Pane:              12,
		OAuthStatus:       robot.OAuthExpired,
		RateLimitStatus:   robot.RateLimitLimited,
		LastActivitySec:   90,
		CooldownRemaining: 45,
		RateLimitCount:    3,
	}

	got := p.formatAgentRow(agent, 4, theme.Current())
	if got == "" {
		t.Fatal("expected non-empty truncated row")
	}
	if lipgloss.Width(got) > 4 {
		t.Fatalf("expected row width <= 4, got %d for %q", lipgloss.Width(got), got)
	}
}

func TestRateLimitPanel_HasError(t *testing.T) {
	t.Parallel()

	p := NewRateLimitPanel()

	if p.HasError() {
		t.Error("new panel should not have error")
	}

	p.SetData(nil, nil)
	if p.HasError() {
		t.Error("panel with nil error should not report HasError")
	}
}
