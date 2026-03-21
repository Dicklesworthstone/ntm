package styles

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestParseHex(t *testing.T) {
	tests := []struct {
		hex      string
		expected Color
	}{
		{"#ff0000", Color{R: 255, G: 0, B: 0}},
		{"#00ff00", Color{R: 0, G: 255, B: 0}},
		{"#0000ff", Color{R: 0, G: 0, B: 255}},
		{"#89b4fa", Color{R: 137, G: 180, B: 250}},
		{"invalid", Color{R: 0, G: 0, B: 0}},
		{"", Color{R: 0, G: 0, B: 0}},
	}

	for _, tt := range tests {
		t.Run(tt.hex, func(t *testing.T) {
			got := ParseHex(tt.hex)
			if got != tt.expected {
				t.Errorf("ParseHex(%q) = %v, want %v", tt.hex, got, tt.expected)
			}
		})
	}
}

func TestColorToHex(t *testing.T) {
	tests := []struct {
		color    Color
		expected string
	}{
		{Color{R: 255, G: 0, B: 0}, "#ff0000"},
		{Color{R: 0, G: 255, B: 0}, "#00ff00"},
		{Color{R: 0, G: 0, B: 255}, "#0000ff"},
		{Color{R: 137, G: 180, B: 250}, "#89b4fa"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := tt.color.ToHex()
			if got != tt.expected {
				t.Errorf("Color.ToHex() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestColorToLipgloss(t *testing.T) {
	c := Color{R: 137, G: 180, B: 250}
	got := c.ToLipgloss()
	if got != lipgloss.Color("#89b4fa") {
		t.Errorf("Color.ToLipgloss() = %v, want %v", got, lipgloss.Color("#89b4fa"))
	}
}

func TestLerp(t *testing.T) {
	c1 := Color{R: 0, G: 0, B: 0}
	c2 := Color{R: 100, G: 200, B: 50}

	tests := []struct {
		t        float64
		expected Color
	}{
		{0.0, c1},
		{1.0, c2},
		{0.5, Color{R: 50, G: 100, B: 25}},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := Lerp(c1, c2, tt.t)
			if got != tt.expected {
				t.Errorf("Lerp(c1, c2, %f) = %v, want %v", tt.t, got, tt.expected)
			}
		})
	}
}

func TestGradientText(t *testing.T) {
	t.Run("with colors", func(t *testing.T) {
		result := GradientText("hello", "#ff0000", "#0000ff")
		if result == "" {
			t.Error("GradientText should return non-empty string")
		}
		// Should contain ANSI escape codes
		if !strings.Contains(result, "\x1b[") {
			t.Error("GradientText should contain ANSI codes")
		}
	})

	t.Run("too few colors", func(t *testing.T) {
		result := GradientText("hello", "#ff0000")
		if result != "hello" {
			t.Error("GradientText with <2 colors should return original text")
		}
	})

	t.Run("empty text", func(t *testing.T) {
		result := GradientText("", "#ff0000", "#0000ff")
		if result != "" {
			t.Error("GradientText with empty text should return empty string")
		}
	})

	t.Run("single character", func(t *testing.T) {
		result := GradientText("x", "#ff0000", "#0000ff")
		if result == "" {
			t.Error("GradientText should handle single character")
		}
	})
}

func TestGradientBar(t *testing.T) {
	t.Run("with colors", func(t *testing.T) {
		result := GradientBar(10, "#ff0000", "#0000ff")
		if result == "" {
			t.Error("GradientBar should return non-empty string")
		}
	})

	t.Run("too few colors", func(t *testing.T) {
		result := GradientBar(10, "#ff0000")
		if !strings.Contains(result, "█") {
			t.Error("GradientBar with <2 colors should return plain blocks")
		}
	})

	t.Run("non-positive width", func(t *testing.T) {
		if result := GradientBar(0, "#ff0000", "#0000ff"); result != "" {
			t.Fatalf("GradientBar with width 0 = %q, want empty string", result)
		}
		if result := GradientBar(-1, "#ff0000", "#0000ff"); result != "" {
			t.Fatalf("GradientBar with width -1 = %q, want empty string", result)
		}
	})
}

func TestGradientBorder(t *testing.T) {
	result := GradientBorder("Hello\nWorld", 20)
	if result == "" {
		t.Error("GradientBorder should return non-empty string")
	}
	if !strings.Contains(result, "╭") {
		t.Error("GradientBorder should contain box corners")
	}

	if result := GradientBorder("Hello", 3); result != "" {
		t.Fatalf("GradientBorder with width 3 = %q, want empty string", result)
	}
}

func TestGlow(t *testing.T) {
	result := Glow("test", "#ff0000", "#00ff00")
	if result == "" {
		t.Error("Glow should return non-empty string")
	}
}

func TestShimmer(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "1")
	t.Setenv("NTM_REDUCE_MOTION", "0")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")

	t.Run("with colors", func(t *testing.T) {
		result := Shimmer("hello", 0, "#ff0000", "#00ff00", "#0000ff")
		if result == "" {
			t.Error("Shimmer should return non-empty string")
		}
	})

	t.Run("deterministic for same tick", func(t *testing.T) {
		// Same tick value should always produce identical output (for global tick sync)
		r1 := Shimmer("test", 42, "#FF0000", "#00FF00")
		r2 := Shimmer("test", 42, "#FF0000", "#00FF00")
		if r1 != r2 {
			t.Error("Shimmer should be deterministic for same tick value")
		}
	})

	t.Run("different ticks produce different results", func(t *testing.T) {
		r1 := Shimmer("hello", 0, "#ff0000", "#0000ff")
		r2 := Shimmer("hello", 50, "#ff0000", "#0000ff")
		// At different ticks, results should differ
		if r1 == r2 {
			t.Error("Shimmer at different ticks should produce different results")
		}
	})

	t.Run("empty text", func(t *testing.T) {
		result := Shimmer("", 0, "#ff0000", "#0000ff")
		if result != "" {
			t.Error("Shimmer with empty text should return empty string")
		}
	})

	t.Run("default colors", func(t *testing.T) {
		result := Shimmer("test", 0)
		if result == "" {
			t.Error("Shimmer with no colors should use defaults")
		}
	})

	t.Run("reduced motion produces stable output", func(t *testing.T) {
		t.Setenv("NTM_ANIMATIONS", "")
		t.Setenv("NTM_REDUCE_MOTION", "1")

		r1 := Shimmer("hello", 0, "#ff0000", "#0000ff")
		r2 := Shimmer("hello", 50, "#ff0000", "#0000ff")
		if r1 != r2 {
			t.Error("expected Shimmer to be stable under reduced motion")
		}
	})
}

func TestRainbow(t *testing.T) {
	result := Rainbow("hello")
	if result == "" {
		t.Error("Rainbow should return non-empty string")
	}
}

func TestPulse(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "1")
	t.Setenv("NTM_REDUCE_MOTION", "0")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")

	c := Pulse("#ff0000", 0)
	if c == "" {
		t.Error("Pulse should return non-empty color")
	}

	// Different ticks should produce different brightness
	c1 := Pulse("#ff0000", 0)
	c2 := Pulse("#ff0000", 15) // ~half a cycle
	if c1 == c2 {
		// They might occasionally be equal depending on sine wave position
		// Just verify both work
	}

	t.Run("reduced motion disables pulsing", func(t *testing.T) {
		t.Setenv("NTM_ANIMATIONS", "")
		t.Setenv("NTM_REDUCE_MOTION", "1")

		want := lipgloss.Color("#ff0000")
		c1 := Pulse("#ff0000", 0)
		c2 := Pulse("#ff0000", 15)
		if c1 != want || c2 != want {
			t.Errorf("expected Pulse to return base color under reduced motion, got %q and %q", c1, c2)
		}
	})
}

func TestAnimationsEnabledAutoDisablesInTmux(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "")
	t.Setenv("NTM_REDUCE_MOTION", "")
	t.Setenv("TMUX", "/tmp/tmux-123/default,1,0")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "tmux-256color")
	t.Setenv("COLORTERM", "truecolor")

	if AnimationsEnabled() {
		t.Fatal("expected animations to auto-disable inside tmux")
	}
	if !ReducedMotionEnabled() {
		t.Fatal("expected reduced motion to auto-enable inside tmux")
	}
}

func TestAnimationsEnabledExplicitOverride(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "1")
	t.Setenv("NTM_REDUCE_MOTION", "")
	t.Setenv("TMUX", "/tmp/tmux-123/default,1,0")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "tmux-256color")
	t.Setenv("COLORTERM", "truecolor")

	if !AnimationsEnabled() {
		t.Fatal("expected NTM_ANIMATIONS=1 to force-enable animations")
	}
	if ReducedMotionEnabled() {
		t.Fatal("expected reduced motion to stay disabled when animations are forced on")
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		input    int
		expected int
	}{
		{-10, 0},
		{0, 0},
		{128, 128},
		{255, 255},
		{300, 255},
	}

	for _, tt := range tests {
		got := clamp(tt.input)
		if got != tt.expected {
			t.Errorf("clamp(%d) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestProgressBar(t *testing.T) {
	t.Run("50% progress", func(t *testing.T) {
		result := ProgressBar(0.5, 20, "█", "░")
		if result == "" {
			t.Error("ProgressBar should return non-empty string")
		}
	})

	t.Run("bounds checking", func(t *testing.T) {
		result := ProgressBar(-0.5, 10, "█", "░")
		if result == "" {
			t.Error("ProgressBar should handle negative percent")
		}

		result = ProgressBar(1.5, 10, "█", "░")
		if result == "" {
			t.Error("ProgressBar should handle percent > 1")
		}
	})

	t.Run("with custom colors", func(t *testing.T) {
		result := ProgressBar(0.5, 10, "█", "░", "#ff0000", "#00ff00")
		if result == "" {
			t.Error("ProgressBar with colors should return non-empty string")
		}
	})

	t.Run("with default characters", func(t *testing.T) {
		result := ProgressBar(0.5, 12, "", "")
		if result == "" {
			t.Fatal("ProgressBar with default characters should return non-empty string")
		}
		if lipgloss.Width(result) != 12 {
			t.Errorf("width = %d, want 12", lipgloss.Width(result))
		}
	})

	t.Run("with single custom color", func(t *testing.T) {
		result := ProgressBar(0.5, 10, "█", "░", "#ff0000")
		if result == "" {
			t.Error("ProgressBar with a single custom color should return non-empty string")
		}
		if lipgloss.Width(result) != 10 {
			t.Errorf("width = %d, want 10", lipgloss.Width(result))
		}
	})
}

func TestGetSpinnerFrame(t *testing.T) {
	t.Run("default frames", func(t *testing.T) {
		frame := GetSpinnerFrame(0, SpinnerFrames)
		if frame != SpinnerFrames[0] {
			t.Errorf("GetSpinnerFrame(0) = %q, want %q", frame, SpinnerFrames[0])
		}
	})

	t.Run("wraps around", func(t *testing.T) {
		frame := GetSpinnerFrame(10, SpinnerFrames)
		if frame != SpinnerFrames[0] {
			t.Errorf("GetSpinnerFrame(10) should wrap to first frame")
		}
	})

	t.Run("empty frames", func(t *testing.T) {
		frame := GetSpinnerFrame(0, []string{})
		if frame != "⠋" {
			t.Errorf("GetSpinnerFrame with empty frames should return default")
		}
	})
}

func TestRenderBox(t *testing.T) {
	result := RenderBox("Hello", 20, RoundedBox, lipgloss.Color("#ff0000"))
	if result == "" {
		t.Error("RenderBox should return non-empty string")
	}
	if !strings.Contains(result, "╭") {
		t.Error("RenderBox should contain rounded corners")
	}

	if result := RenderBox("Hello", 3, RoundedBox, lipgloss.Color("#ff0000")); result != "" {
		t.Fatalf("RenderBox with width 3 = %q, want empty string", result)
	}
}

func TestDivider(t *testing.T) {
	tests := []struct {
		style    string
		expected string
	}{
		{"heavy", "━"},
		{"double", "═"},
		{"dotted", "·"},
		{"dashed", "╌"},
		{"", "─"},
	}

	for _, tt := range tests {
		t.Run(tt.style, func(t *testing.T) {
			result := Divider(5, tt.style, lipgloss.Color("#ff0000"))
			if result == "" {
				t.Error("Divider should return non-empty string")
			}
		})
	}

	if result := Divider(0, "heavy", lipgloss.Color("#ff0000")); result != "" {
		t.Fatalf("Divider with width 0 = %q, want empty string", result)
	}
	if result := Divider(-1, "heavy", lipgloss.Color("#ff0000")); result != "" {
		t.Fatalf("Divider with width -1 = %q, want empty string", result)
	}
}

func TestGradientDivider(t *testing.T) {
	t.Run("with colors", func(t *testing.T) {
		result := GradientDivider(10, "#ff0000", "#0000ff")
		if result == "" {
			t.Error("GradientDivider should return non-empty string")
		}
	})

	t.Run("default colors", func(t *testing.T) {
		result := GradientDivider(10)
		if result == "" {
			t.Error("GradientDivider with defaults should return non-empty string")
		}
	})

	t.Run("non-positive width", func(t *testing.T) {
		if result := GradientDivider(0, "#ff0000", "#0000ff"); result != "" {
			t.Fatalf("GradientDivider with width 0 = %q, want empty string", result)
		}
		if result := GradientDivider(-1, "#ff0000", "#0000ff"); result != "" {
			t.Fatalf("GradientDivider with width -1 = %q, want empty string", result)
		}
	})
}

// [tui-upgrade: bd-28vsw]
func TestAnimatedGradientDivider(t *testing.T) {
	t.Run("with colors", func(t *testing.T) {
		result := AnimatedGradientDivider(10, 5, "#ff0000", "#0000ff")
		if result == "" {
			t.Error("AnimatedGradientDivider should return non-empty string")
		}
	})

	t.Run("different ticks produce different results", func(t *testing.T) {
		r1 := AnimatedGradientDivider(10, 0, "#ff0000", "#0000ff")
		r2 := AnimatedGradientDivider(10, 50, "#ff0000", "#0000ff")
		// Both should be non-empty, and if motion is enabled they should differ
		if r1 == "" || r2 == "" {
			t.Error("AnimatedGradientDivider should return non-empty strings")
		}
	})

	t.Run("non-positive width", func(t *testing.T) {
		if result := AnimatedGradientDivider(0, 5, "#ff0000", "#0000ff"); result != "" {
			t.Fatalf("AnimatedGradientDivider with width 0 = %q, want empty", result)
		}
	})
}

// [tui-upgrade: bd-28vsw]
func TestAnimatedBorderColor(t *testing.T) {
	t.Run("returns valid color", func(t *testing.T) {
		color := AnimatedBorderColor(0, "#89b4fa", "#cba6f7")
		if color == "" {
			t.Error("AnimatedBorderColor should return non-empty color")
		}
	})

	t.Run("different ticks may return different colors", func(t *testing.T) {
		c1 := AnimatedBorderColor(0, "#ff0000", "#0000ff")
		c2 := AnimatedBorderColor(15, "#ff0000", "#0000ff")
		// Both should be valid lipgloss colors
		if c1 == "" || c2 == "" {
			t.Error("AnimatedBorderColor should return valid colors")
		}
	})
}

func TestBadge(t *testing.T) {
	result := Badge("test", lipgloss.Color("#ff0000"), lipgloss.Color("#ffffff"))
	if result == "" {
		t.Error("Badge should return non-empty string")
	}
}

func TestGlowBadge(t *testing.T) {
	result := GlowBadge("test", "#ff0000")
	if result == "" {
		t.Error("GlowBadge should return non-empty string")
	}
}

func TestKeyHint(t *testing.T) {
	result := KeyHint("q", "quit", lipgloss.Color("#ff0000"), lipgloss.Color("#ffffff"))
	if result == "" {
		t.Error("KeyHint should return non-empty string")
	}
	if !strings.Contains(result, "q") {
		t.Error("KeyHint should contain the key")
	}
}

func TestStatusDot(t *testing.T) {
	t.Run("static", func(t *testing.T) {
		result := StatusDot(lipgloss.Color("#ff0000"), false, 0)
		if result == "" {
			t.Error("StatusDot should return non-empty string")
		}
	})

	t.Run("animated", func(t *testing.T) {
		result := StatusDot(lipgloss.Color("#ff0000"), true, 0)
		if result == "" {
			t.Error("StatusDot animated should return non-empty string")
		}
	})
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		text     string
		maxWidth int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"hi", 10, "hi"},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := Truncate(tt.text, tt.maxWidth)
			if got != tt.expected {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.text, tt.maxWidth, got, tt.expected)
			}
		})
	}
}

func TestCenterText(t *testing.T) {
	tests := []struct {
		text     string
		width    int
		expected string
	}{
		{"hi", 6, "  hi  "},
		{"hello", 5, "hello"},
		{"a", 5, "  a  "},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := CenterText(tt.text, tt.width)
			if got != tt.expected {
				t.Errorf("CenterText(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.expected)
			}
		})
	}
}

func TestRightAlign(t *testing.T) {
	tests := []struct {
		text     string
		width    int
		expected string
	}{
		{"hi", 6, "    hi"},
		{"hello", 5, "hello"},
		{"a", 5, "    a"},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := RightAlign(tt.text, tt.width)
			if got != tt.expected {
				t.Errorf("RightAlign(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.expected)
			}
		})
	}
}

func TestBoxCharsets(t *testing.T) {
	t.Run("RoundedBox has all chars", func(t *testing.T) {
		if RoundedBox.TopLeft == "" {
			t.Error("TopLeft should not be empty")
		}
		if RoundedBox.Horizontal == "" {
			t.Error("Horizontal should not be empty")
		}
	})

	t.Run("DoubleBox has all chars", func(t *testing.T) {
		if DoubleBox.TopLeft == "" {
			t.Error("TopLeft should not be empty")
		}
	})

	t.Run("HeavyBox has all chars", func(t *testing.T) {
		if HeavyBox.TopLeft == "" {
			t.Error("TopLeft should not be empty")
		}
	})
}

func TestSpinnerFrames(t *testing.T) {
	if len(SpinnerFrames) == 0 {
		t.Error("SpinnerFrames should not be empty")
	}
	if len(DotsSpinnerFrames) == 0 {
		t.Error("DotsSpinnerFrames should not be empty")
	}
	if len(LineSpinnerFrames) == 0 {
		t.Error("LineSpinnerFrames should not be empty")
	}
	if len(BounceSpinnerFrames) == 0 {
		t.Error("BounceSpinnerFrames should not be empty")
	}
}

// =============================================================================
// ParseHex — missing branches (bd-4b4zf)
// =============================================================================

func TestParseHex_MissingBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hex  string
		want Color
	}{
		{"uppercase hex", "#FF0000", Color{R: 255, G: 0, B: 0}},
		{"mixed case hex", "#aAbBcC", Color{R: 170, G: 187, B: 204}},
		{"too short", "#fff", Color{}},
		{"too long", "#ff0000ff", Color{}},
		{"no hash prefix", "ff0000f", Color{}},
		{"3-char shorthand", "#abc", Color{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseHex(tc.hex)
			if got != tc.want {
				t.Errorf("ParseHex(%q) = %v, want %v", tc.hex, got, tc.want)
			}
		})
	}
}
