package styles

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

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
		result := TextBadge("claude", "#89b4fa", "#1e1e2e", opt)
		if result == "" {
			t.Errorf("TextBadge with opts[%d] returned empty string", i)
		}
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

func TestTruncateToWidth_AllRunesExhausted(t *testing.T) {
	t.Parallel()

	// A wide CJK character (width 2) with maxWidth=1 should exhaust all runes
	got := truncateToWidth("中", 1)
	if got != "" {
		t.Errorf("truncateToWidth(CJK, 1) = %q, want empty", got)
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
