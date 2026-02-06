package styles

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestAgentBadge(t *testing.T) {
	tests := []struct {
		agentType string
		wantLabel string
	}{
		{"claude", "claude"},
		{"cc", "claude"},
		{"codex", "codex"},
		{"cod", "codex"},
		{"gemini", "gemini"},
		{"gmi", "gemini"},
		{"user", "user"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			result := AgentBadge(tt.agentType)
			if result == "" {
				t.Error("AgentBadge returned empty string")
			}
			// Badge should contain the label
			if !strings.Contains(result, tt.wantLabel) {
				t.Errorf("AgentBadge(%q) should contain %q", tt.agentType, tt.wantLabel)
			}
		})
	}
}

func TestAgentBadgeWithCount(t *testing.T) {
	tests := []struct {
		agentType string
		count     int
	}{
		{"claude", 3},
		{"codex", 1},
		{"gemini", 5},
		{"user", 0},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			result := AgentBadgeWithCount(tt.agentType, tt.count)
			if result == "" {
				t.Error("AgentBadgeWithCount returned empty string")
			}
		})
	}
}

func TestStatusBadge(t *testing.T) {
	statuses := []string{
		"success", "ok", "done",
		"running", "active",
		"idle", "waiting",
		"warning", "warn",
		"error", "failed",
		"pending",
		"disabled",
		"blocked",
		"unknown",
	}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			result := StatusBadge(status)
			if result == "" {
				t.Errorf("StatusBadge(%q) returned empty string", status)
			}
		})
	}
}

func TestStatusBadgeIcon(t *testing.T) {
	statuses := []string{
		"success", "running", "idle", "warning", "error", "pending", "blocked", "unknown",
	}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			result := StatusBadgeIcon(status)
			if result == "" {
				t.Errorf("StatusBadgeIcon(%q) returned empty string", status)
			}
		})
	}
}

func TestPriorityBadge(t *testing.T) {
	for priority := 0; priority <= 5; priority++ {
		name := fmt.Sprintf("P%d", priority)
		p := priority // capture for closure
		t.Run(name, func(t *testing.T) {
			result := PriorityBadge(p)
			if result == "" {
				t.Errorf("PriorityBadge(%d) returned empty string", p)
			}
			// Should contain P followed by number
			expected := fmt.Sprintf("P%d", p)
			if !strings.Contains(result, expected) {
				t.Errorf("PriorityBadge(%d) should contain %q", p, expected)
			}
		})
	}
}

func TestCountBadge(t *testing.T) {
	tests := []struct {
		count int
	}{
		{0},
		{1},
		{99},
		{999},
	}

	for _, tt := range tests {
		result := CountBadge(tt.count, "#89b4fa", "#1e1e2e")
		if result == "" {
			t.Errorf("CountBadge(%d) returned empty string", tt.count)
		}
	}
}

func TestHealthBadge(t *testing.T) {
	statuses := []string{
		"ok", "healthy", "warning", "drift", "critical", "no_baseline", "unavailable", "unknown",
	}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			result := HealthBadge(status)
			if result == "" {
				t.Errorf("HealthBadge(%q) returned empty string", status)
			}
		})
	}
}

func TestIssueTypeBadge(t *testing.T) {
	types := []string{
		"epic", "feature", "task", "bug", "chore", "unknown",
	}

	for _, issueType := range types {
		t.Run(issueType, func(t *testing.T) {
			result := IssueTypeBadge(issueType)
			if result == "" {
				t.Errorf("IssueTypeBadge(%q) returned empty string", issueType)
			}
			// Should contain the type name
			if !strings.Contains(result, issueType) {
				t.Errorf("IssueTypeBadge(%q) should contain the type name", issueType)
			}
		})
	}
}

func TestModelBadge(t *testing.T) {
	models := []struct {
		model string
		want  string
	}{
		{"claude-3-opus", "opus"},
		{"gpt-4o-mini", "4o"},
		{"gemini-1.5-pro", "g1.5"},
		{"unknown-model", "unknown-model"},
	}

	for _, tt := range models {
		t.Run(tt.model, func(t *testing.T) {
			result := ModelBadge(tt.model)
			if result == "" {
				t.Errorf("ModelBadge(%q) returned empty string", tt.model)
			}
			if !strings.Contains(result, tt.want) {
				t.Errorf("ModelBadge(%q) should contain %q", tt.model, tt.want)
			}
		})
	}
}

func TestTokenVelocityBadge(t *testing.T) {
	values := []float64{0, 1500, 4500, 9000}
	for _, v := range values {
		result := TokenVelocityBadge(v)
		if result == "" {
			t.Errorf("TokenVelocityBadge(%f) returned empty string", v)
		}
		if !strings.Contains(result, "tpm") {
			t.Errorf("TokenVelocityBadge(%f) should contain tpm", v)
		}
	}
}

func TestTokensPerSecondBadge(t *testing.T) {
	values := []float64{0, 10, 42.1, 120}
	for _, v := range values {
		result := TokensPerSecondBadge(v)
		if result == "" {
			t.Errorf("TokensPerSecondBadge(%f) returned empty string", v)
		}
		if !strings.Contains(result, "tok/s") {
			t.Errorf("TokensPerSecondBadge(%f) should contain tok/s", v)
		}
	}
}

func TestMemoryUsageBadge(t *testing.T) {
	values := []int64{0, 1024, 8 * 1024 * 1024 * 1024}
	for _, v := range values {
		result := MemoryUsageBadge(v)
		if result == "" {
			t.Errorf("MemoryUsageBadge(%d) returned empty string", v)
		}
	}
}

func TestAlertSeverityBadge(t *testing.T) {
	severities := []string{"critical", "high", "medium", "low", "info", "other", "p0", "sev2"}
	for _, sev := range severities {
		t.Run(sev, func(t *testing.T) {
			result := AlertSeverityBadge(sev)
			if result == "" {
				t.Errorf("AlertSeverityBadge(%q) returned empty string", sev)
			}
			label := severityLabel(sev)
			if label != "" && !strings.Contains(strings.ToLower(result), label) && sev != "other" {
				t.Errorf("AlertSeverityBadge(%q) should include label %q", sev, label)
			}
		})
	}
}

func severityLabel(sev string) string {
	switch strings.ToLower(sev) {
	case "critical", "crit", "p0", "sev0":
		return "critical"
	case "high", "p1", "sev1":
		return "high"
	case "medium", "med", "p2", "sev2":
		return "medium"
	case "low", "p3", "sev3":
		return "low"
	case "info":
		return "info"
	default:
		return ""
	}
}

func TestBadgeOptions(t *testing.T) {
	// Test with different badge styles
	opts := []BadgeOptions{
		{Style: BadgeStyleDefault, Bold: true, ShowIcon: true},
		{Style: BadgeStyleCompact, Bold: false, ShowIcon: false},
		{Style: BadgeStylePill, Bold: true, ShowIcon: true},
	}

	for i, opt := range opts {
		result := AgentBadge("claude", opt)
		if result == "" {
			t.Errorf("AgentBadge with opts[%d] returned empty string", i)
		}
	}
}

func TestBadgeGroup(t *testing.T) {
	b1 := AgentBadge("claude")
	b2 := StatusBadge("running")
	b3 := PriorityBadge(1)

	result := BadgeGroup(b1, b2, b3)
	if result == "" {
		t.Error("BadgeGroup returned empty string")
	}
	// Should contain all three badges separated by space
	if !strings.Contains(result, " ") {
		t.Error("BadgeGroup should separate badges with spaces")
	}
}

func TestBadgeBar(t *testing.T) {
	b1 := AgentBadge("claude")
	b2 := StatusBadge("running")

	result := BadgeBar(b1, b2)
	if result == "" {
		t.Error("BadgeBar returned empty string")
	}
	// Should contain double space separator
	if !strings.Contains(result, "  ") {
		t.Error("BadgeBar should separate badges with double spaces")
	}
}

func TestTextBadge(t *testing.T) {
	result := TextBadge("custom", "#89b4fa", "#1e1e2e")
	if result == "" {
		t.Error("TextBadge returned empty string")
	}
	if !strings.Contains(result, "custom") {
		t.Error("TextBadge should contain the text")
	}
}

func TestMiniBar(t *testing.T) {
	p := DefaultMiniBarPalette()
	bar := MiniBar(0.75, 6, p)
	if bar == "" {
		t.Fatal("MiniBar returned empty string")
	}
	if w := lipgloss.Width(bar); w != 6 {
		t.Fatalf("MiniBar width = %d, want 6", w)
	}

	// Four-tier threshold: ensure mid-high band uses provided MidHigh
	custom := p
	custom.MidHigh = lipgloss.Color("#ffff00")
	MiniBar(0.7, 4, custom) // should not panic; color check is visual-only here

	// Clamp extremes
	if w := lipgloss.Width(MiniBar(1.5, 4)); w != 4 {
		t.Fatalf("MiniBar should clamp values above 1; width=%d", w)
	}
	if w := lipgloss.Width(MiniBar(-1, 3)); w != 3 {
		t.Fatalf("MiniBar should clamp values below 0; width=%d", w)
	}
}

func TestRankBadge(t *testing.T) {
	tests := []int{1, 2, 3, 4}
	for _, rank := range tests {
		rank := rank
		t.Run(fmt.Sprintf("rank_%d", rank), func(t *testing.T) {
			out := RankBadge(rank)
			if out == "" {
				t.Fatalf("RankBadge(%d) returned empty string", rank)
			}
			if !strings.Contains(out, fmt.Sprintf("#%d", rank)) {
				t.Fatalf("RankBadge(%d) missing label", rank)
			}
		})
	}
}

func TestFixedWidthBadge(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		fixedWidth int
		wantWidth  int
	}{
		{"short_text_padded", "opus", 8, 8},
		{"exact_width", "gemini-2", 8, 8},
		{"long_text_truncated", "very-long-model-name", 8, 8},
		{"single_char", "x", 8, 8},
		{"empty_string", "", 8, 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ModelBadge(tt.text, BadgeOptions{
				Style:      BadgeStyleCompact,
				ShowIcon:   false,
				FixedWidth: tt.fixedWidth,
			})
			got := lipgloss.Width(result)
			if got != tt.wantWidth {
				t.Errorf("ModelBadge(%q, FixedWidth=%d) width = %d, want %d",
					tt.text, tt.fixedWidth, got, tt.wantWidth)
			}
		})
	}
}

func TestTruncateBadgeText(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short_no_truncate", "opus", 8, "opus"},
		{"exact_length", "12345678", 8, "12345678"},
		{"truncate_with_ellipsis", "very-long-name", 8, "very-lo…"},
		{"empty_string", "", 8, ""},
		{"single_char_limit", "hello", 1, "…"},
		{"zero_limit", "hello", 0, ""},
		{"unicode_string", "日本語テスト", 4, "日…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateBadgeText(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateBadgeText(%q, %d) = %q, want %q",
					tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestModelBadgeWidthConstant(t *testing.T) {
	// Verify the constant is a reasonable value
	if ModelBadgeWidth < 4 || ModelBadgeWidth > 12 {
		t.Errorf("ModelBadgeWidth = %d, expected between 4 and 12", ModelBadgeWidth)
	}

	// Verify different model variants render to the same width
	variants := []string{"opus", "sonnet", "haiku", "gpt-4o", "gemini-1.5", "claude-3-sonnet"}
	widths := make(map[int]bool)

	for _, v := range variants {
		badge := ModelBadge(v, BadgeOptions{
			Style:      BadgeStyleCompact,
			ShowIcon:   false,
			FixedWidth: ModelBadgeWidth,
		})
		w := lipgloss.Width(badge)
		widths[w] = true
	}

	if len(widths) != 1 {
		t.Errorf("ModelBadge with FixedWidth produced inconsistent widths: %v", widths)
	}
}
