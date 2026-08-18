package tutorial

import (
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
)

func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return re.ReplaceAllString(s, "")
}

func TestClamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"zero", 0, 0},
		{"mid range", 128, 128},
		{"max", 255, 255},
		{"above max", 256, 255},
		{"well above max", 1000, 255},
		{"negative", -1, 0},
		{"very negative", -100, 0},
		{"one", 1, 1},
		{"254", 254, 254},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := clamp(tc.input)
			if got != tc.want {
				t.Errorf("clamp(%d) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestVisibleLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty string", "", 0},
		{"plain ASCII", "hello", 5},
		{"with ANSI color", "\x1b[31mred\x1b[0m", 3},
		{"nested ANSI", "\x1b[1m\x1b[32mbold green\x1b[0m\x1b[0m", 10},
		{"no ANSI", "plain text", 10},
		{"only ANSI", "\x1b[31m\x1b[0m", 0},
		{"mixed", "before\x1b[31mred\x1b[0mafter", 14},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := visibleLength(tc.input)
			if got != tc.want {
				t.Errorf("visibleLength(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestNewTutorialModel(t *testing.T) {
	m := New()

	if m.currentSlide != SlideWelcome {
		t.Errorf("Expected initial slide Welcome, got %v", m.currentSlide)
	}
	if m.width != 80 {
		t.Errorf("Expected default width 80, got %d", m.width)
	}
	if len(m.slideStates) != SlideCount {
		t.Errorf("Expected %d slide states, got %d", SlideCount, len(m.slideStates))
	}
}

func TestNewTutorialModelAutoSkipsAnimationsInTmux(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "")
	t.Setenv("NTM_REDUCE_MOTION", "")
	t.Setenv("TMUX", "/tmp/tmux-123/default,1,0")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "tmux-256color")
	t.Setenv("COLORTERM", "truecolor")

	m := New()
	if !m.skipAnimations {
		t.Fatal("expected tutorial to auto-skip animations inside tmux")
	}
	if cmd := m.Init(); cmd != nil {
		t.Fatal("expected tutorial init to avoid scheduling a tick when animations are skipped")
	}
}

func TestNewTutorialModelWithOptions(t *testing.T) {
	m := New(WithSkipAnimations(), WithStartSlide(SlideCommands))

	if !m.skipAnimations {
		t.Error("Expected skipAnimations to be true")
	}
	if m.currentSlide != SlideCommands {
		t.Errorf("Expected start slide Commands, got %v", m.currentSlide)
	}
}

func TestTutorialSlideCount(t *testing.T) {
	if SlideCount != 9 {
		t.Errorf("Expected 9 slides, got %d", SlideCount)
	}
}

func updateModel(m Model, msg tea.Msg) Model {
	newM, _ := m.Update(msg)
	if modelPtr, ok := newM.(*Model); ok {
		return *modelPtr
	}
	return newM.(Model)
}

func TestTutorialNavigation_Next(t *testing.T) {
	m := New(WithSkipAnimations())
	initialSlide := m.currentSlide

	// Simulate 'right' key
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRight})

	if m.currentSlide != initialSlide+1 {
		t.Errorf("Expected slide %v, got %v", initialSlide+1, m.currentSlide)
	}

	// Simulate 'enter' key
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.currentSlide != initialSlide+2 {
		t.Errorf("Expected slide %v, got %v", initialSlide+2, m.currentSlide)
	}
}

func TestTutorialNavigation_Prev(t *testing.T) {
	m := New(WithSkipAnimations(), WithStartSlide(SlideCommands))
	initialSlide := m.currentSlide

	// Simulate 'left' key
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyLeft})

	if m.currentSlide != initialSlide-1 {
		t.Errorf("Expected slide %v, got %v", initialSlide-1, m.currentSlide)
	}
}

func TestTutorialNavigation_Jump(t *testing.T) {
	m := New(WithSkipAnimations())

	// Jump to slide 5 (key '5')
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})

	if m.currentSlide != SlideQuickStart {
		t.Errorf("Expected slide QuickStart, got %v", m.currentSlide)
	}
}

func TestTutorialTransitions(t *testing.T) {
	m := New() // Animations enabled

	// Trigger next slide
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRight})

	// Since we disabled slide transitions in handleKey ("Always do instant transitions"),
	// transitioning should be false immediately?
	if m.transitioning {
		t.Error("Expected instant transition (transitioning=false)")
	}
	if m.currentSlide != SlideProblem {
		t.Errorf("Expected slide Problem, got %v", m.currentSlide)
	}
}

func TestTutorialSkipAnimation(t *testing.T) {
	m := New()
	// Current slide state should have typingDone = false initially
	state := m.slideStates[m.currentSlide]
	state.typingContent = []string{"Hello"}
	state.typingDone = false

	// Simulate 's' key
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})

	state = m.slideStates[m.currentSlide]
	if !state.typingDone {
		t.Error("Expected typingDone to be true after skipping")
	}
}

func TestSlideContent_View(t *testing.T) {
	m := New(WithSkipAnimations())

	// Render view
	view := m.View()

	if view == "" {
		t.Error("Expected non-empty view")
	}

	// Should contain "Welcome" or something from the slide
	if !strings.Contains(view, "Welcome") && !strings.Contains(view, "journey") {
		// We need to advance ticks
		for i := 0; i < 50; i++ {
			m = updateModel(m, TickMsg(time.Now()))
		}

		view := stripANSI(m.View())
		if !strings.Contains(view, "journey") {
			t.Logf("View output (stripped): %s", view)
			t.Error("Expected view to contain 'journey' after ticks")
		}
	}
}

// =============================================================================
// ascii.go helpers
// =============================================================================

func TestItoa(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input int
		want  string
	}{
		{"zero", 0, "0"},
		{"single digit", 7, "7"},
		{"double digit", 42, "42"},
		{"triple digit", 255, "255"},
		{"one hundred", 100, "100"},
		{"ten", 10, "10"},
		{"ninety nine", 99, "99"},
		{"negative single", -5, "-5"},
		{"negative double", -42, "-42"},
		{"negative triple", -128, "-128"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := itoa(tc.input)
			if got != tc.want {
				t.Errorf("itoa(%d) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestContainsLetters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"only spaces", "   ", false},
		{"only digits", "12345", false},
		{"only symbols", "+-|/\\", false},
		{"only pipes and spaces", "  |  |  |  ", false},
		{"lowercase", "abc", true},
		{"uppercase", "ABC", true},
		{"mixed case", "aBc", true},
		{"letter in symbols", "+--A--+", true},
		{"single letter", "a", true},
		{"box drawing chars", "+---------+", false},
		{"letter z boundary", "z", true},
		{"letter Z boundary", "Z", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := containsLetters(tc.input)
			if got != tc.want {
				t.Errorf("containsLetters(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestCenterText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		width int
		check func(string) bool
		desc  string
	}{
		{
			"plain text narrower than width",
			"hello", 20,
			func(s string) bool { return strings.HasPrefix(s, "       ") && strings.Contains(s, "hello") },
			"should have left padding",
		},
		{
			"text wider than width",
			"this is a very long text", 5,
			func(s string) bool { return s == "this is a very long text" },
			"should return text unchanged",
		},
		{
			"text equal to width",
			"12345", 5,
			func(s string) bool { return s == "12345" },
			"should return text unchanged",
		},
		{
			"empty text",
			"", 20,
			func(s string) bool { return strings.TrimRight(s, " ") == "" && len(s) == 10 },
			"should be padding only",
		},
		{
			"ANSI colored text",
			"\x1b[31mred\x1b[0m", 20,
			func(s string) bool {
				// visibleLength is 3, so padding should be (20-3)/2 = 8
				return strings.HasPrefix(s, "        \x1b[31m")
			},
			"should pad based on visible length not raw length",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := centerText(tc.text, tc.width)
			if !tc.check(got) {
				t.Errorf("centerText(%q, %d) = %q; %s", tc.text, tc.width, got, tc.desc)
			}
		})
	}
}

func TestCenterTextWidth(t *testing.T) {
	t.Parallel()

	// centerTextWidth is identical to centerText but defined in slides.go
	got := centerTextWidth("test", 20)
	stripped := stripANSI(got)
	if !strings.Contains(stripped, "test") {
		t.Errorf("centerTextWidth should contain 'test', got %q", stripped)
	}
	// Padding = (20 - 4) / 2 = 8
	if !strings.HasPrefix(got, "        ") {
		t.Errorf("centerTextWidth should have 8-space prefix, got %q", got)
	}
}

func TestApplyColor(t *testing.T) {
	t.Parallel()

	got := applyColor("hello", "#ff0000")
	// Should wrap in ANSI color codes
	if !strings.HasPrefix(got, "\x1b[38;2;") {
		t.Errorf("applyColor should start with ANSI color prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("applyColor should end with reset, got %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("applyColor should contain the text, got %q", got)
	}

	// Verify the color components are present (255;0;0 for #ff0000)
	if !strings.Contains(got, "255;0;0m") {
		t.Errorf("applyColor(#ff0000) should contain 255;0;0, got %q", got)
	}
}

func TestApplyColor_DifferentColors(t *testing.T) {
	t.Parallel()

	white := applyColor("x", "#ffffff")
	if !strings.Contains(white, "255;255;255m") {
		t.Errorf("applyColor(#ffffff) should contain 255;255;255, got %q", white)
	}

	black := applyColor("x", "#000000")
	if !strings.Contains(black, "0;0;0m") {
		t.Errorf("applyColor(#000000) should contain 0;0;0, got %q", black)
	}
}

// =============================================================================
// animations.go: transition effects
// =============================================================================

func TestTransitionDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ttype    TransitionType
		expected int
	}{
		{"fade out", TransitionFadeOut, 15},
		{"fade in", TransitionFadeIn, 15},
		{"slide left", TransitionSlideLeft, 12},
		{"slide right", TransitionSlideRight, 12},
		{"zoom in", TransitionZoomIn, 18},
		{"zoom out", TransitionZoomOut, 18},
		{"dissolve", TransitionDissolve, 10},
		{"none", TransitionNone, 0},
		{"unknown", TransitionType(99), 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := transitionDuration(tc.ttype)
			if got != tc.expected {
				t.Errorf("transitionDuration(%v) = %d, want %d", tc.ttype, got, tc.expected)
			}
		})
	}
}

// =============================================================================
// animations.go: text effects
// =============================================================================

func TestWaveText(t *testing.T) {
	t.Parallel()

	colors := []string{"#89b4fa", "#cba6f7", "#f5c2e7"}

	// Non-empty output
	got := WaveText("hello world", 10, 1.0, colors)
	if got == "" {
		t.Error("WaveText should produce non-empty output")
	}

	stripped := stripANSI(got)
	if !strings.Contains(stripped, "hello world") {
		t.Errorf("WaveText stripped should contain original text, got %q", stripped)
	}

	// Deterministic: same inputs = same output
	got2 := WaveText("hello world", 10, 1.0, colors)
	if got != got2 {
		t.Error("WaveText should be deterministic for same inputs")
	}

	// Different ticks produce different ANSI output
	got3 := WaveText("hello world", 20, 1.0, colors)
	if got == got3 {
		t.Error("WaveText should vary with tick")
	}

	// Spaces preserved
	gotSpaces := WaveText("a b", 0, 1.0, colors)
	stripped2 := stripANSI(gotSpaces)
	if !strings.Contains(stripped2, " ") {
		t.Error("WaveText should preserve spaces")
	}
}

func TestPulseText(t *testing.T) {
	t.Parallel()

	got := PulseText("hello", 0, "#ff0000")
	if got == "" {
		t.Error("PulseText should produce non-empty output")
	}

	stripped := stripANSI(got)
	if stripped != "hello" {
		t.Errorf("PulseText stripped should be 'hello', got %q", stripped)
	}

	// Different ticks produce different brightness
	got1 := PulseText("hello", 0, "#ff0000")
	// tick=0 → pulse = 0.6 + 0.4*sin(0) = 0.6
	// tick=15 → pulse = 0.6 + 0.4*sin(1.5) ≈ 0.999
	got2 := PulseText("hello", 15, "#ff0000")
	if got1 == got2 {
		t.Error("PulseText should vary with tick")
	}

	// Verify ANSI wrapping
	if !strings.HasPrefix(got, "\x1b[38;2;") {
		t.Error("PulseText should wrap in ANSI color")
	}
}

func TestProgressDots(t *testing.T) {
	t.Parallel()

	got := ProgressDots(2, 5, 0)
	if got == "" {
		t.Error("ProgressDots should produce non-empty output")
	}

	stripped := stripANSI(got)
	// Should contain completed dots (●), current dot (◉), and future dots (○)
	completedCount := strings.Count(stripped, "●")
	currentCount := strings.Count(stripped, "◉")
	futureCount := strings.Count(stripped, "○")

	if completedCount != 2 {
		t.Errorf("ProgressDots(2,5) should have 2 completed dots, got %d", completedCount)
	}
	if currentCount != 1 {
		t.Errorf("ProgressDots(2,5) should have 1 current dot, got %d", currentCount)
	}
	if futureCount != 2 {
		t.Errorf("ProgressDots(2,5) should have 2 future dots, got %d", futureCount)
	}
}

func TestProgressDots_EdgeCases(t *testing.T) {
	t.Parallel()

	// First position
	got := ProgressDots(0, 3, 0)
	stripped := stripANSI(got)
	if strings.Count(stripped, "●") != 0 {
		t.Error("ProgressDots(0,3) should have 0 completed dots")
	}
	if strings.Count(stripped, "◉") != 1 {
		t.Error("ProgressDots(0,3) should have 1 current dot")
	}

	// Last position
	gotLast := ProgressDots(2, 3, 0)
	strippedLast := stripANSI(gotLast)
	if strings.Count(strippedLast, "●") != 2 {
		t.Errorf("ProgressDots(2,3) should have 2 completed dots, got %d", strings.Count(strippedLast, "●"))
	}
}

// =============================================================================
// animations.go: Particle
// =============================================================================

func TestParticleRender(t *testing.T) {
	t.Parallel()

	// Particle with full life should render
	p := Particle{
		Life:    30,
		MaxLife: 30,
		Char:    "★",
		Color:   "#ff0000",
	}
	got := p.Render()
	if got == "" {
		t.Error("Particle with full life should render non-empty")
	}
	if !strings.Contains(got, "★") {
		t.Errorf("Particle.Render should contain char '★', got %q", got)
	}

	// Particle with low life (< 30% alpha) should render empty
	pLow := Particle{
		Life:    2,
		MaxLife: 30,
		Char:    "★",
		Color:   "#ff0000",
	}
	gotLow := pLow.Render()
	if gotLow != "" {
		t.Errorf("Particle with life/max < 0.3 should render empty, got %q", gotLow)
	}

	// Particle with exactly 30% life should render
	pBorder := Particle{
		Life:    9,
		MaxLife: 30,
		Char:    "★",
		Color:   "#ff0000",
	}
	gotBorder := pBorder.Render()
	if gotBorder == "" {
		t.Error("Particle at exactly 30% life should still render")
	}
}

func TestParticleUpdate(t *testing.T) {
	t.Parallel()

	p := Particle{
		X:       10.0,
		Y:       10.0,
		VX:      1.0,
		VY:      0.0,
		Life:    30,
		Gravity: 0.1,
	}

	p.Update()

	if p.X != 11.0 {
		t.Errorf("X should be 11.0 after update, got %f", p.X)
	}
	if p.VY != 0.1 {
		t.Errorf("VY should be 0.1 after gravity, got %f", p.VY)
	}
	if p.Y != 10.1 {
		t.Errorf("Y should be 10.1 after update, got %f", p.Y)
	}
	if p.Life != 29 {
		t.Errorf("Life should be 29 after update, got %d", p.Life)
	}
}

func TestParticleUpdate_Friction(t *testing.T) {
	t.Parallel()

	p := Particle{
		VX:       2.0,
		VY:       0.0,
		Friction: 0.5,
		Gravity:  0.0,
		Life:     10,
	}

	p.Update()
	if math.Abs(p.VX-1.0) > 0.001 {
		t.Errorf("VX should be 1.0 after 50%% friction, got %f", p.VX)
	}
}

// =============================================================================
// animations.go: TypingAnimation
// =============================================================================

// =============================================================================
// animations.go: RevealAnimation
// =============================================================================

// =============================================================================
// ascii.go: Render functions
// =============================================================================

func TestRenderAnimatedLogo(t *testing.T) {
	t.Parallel()

	// At tick=0, only first few lines revealed
	got0 := RenderAnimatedLogo(0, 80)
	// At high tick, logo + tagline + subtitle
	got100 := RenderAnimatedLogo(100, 80)

	stripped100 := stripANSI(got100)
	// The logo uses Unicode block characters (███) to render "NTM", not literal text.
	// Check for block chars and the tagline instead.
	if !strings.Contains(stripped100, "███") {
		t.Errorf("logo at tick=100 should contain block characters, got %q", stripped100)
	}

	// High tick reveals tagline
	if !strings.Contains(stripped100, "Named Tmux Manager") {
		t.Errorf("logo at tick=100 should contain tagline, got %q", stripped100)
	}

	// Low tick should have less content
	if len(got0) >= len(got100) {
		t.Error("tick=0 should produce less content than tick=100")
	}
}

func TestRenderAnimatedChaosDiagram(t *testing.T) {
	t.Parallel()

	got := RenderAnimatedChaosDiagram(10, 80)
	if got == "" {
		t.Error("should produce non-empty output")
	}

	stripped := stripANSI(got)
	if !strings.Contains(stripped, "Claude") {
		t.Errorf("chaos diagram should contain 'Claude', got %q", stripped)
	}
}

func TestRenderAnimatedOrderDiagram(t *testing.T) {
	t.Parallel()

	// At low tick, few lines revealed
	got5 := RenderAnimatedOrderDiagram(5, 80)
	// At high tick, all lines
	got100 := RenderAnimatedOrderDiagram(100, 80)

	if len(got5) >= len(got100) {
		t.Error("low tick should produce less content than high tick")
	}

	stripped := stripANSI(got100)
	if !strings.Contains(stripped, "Session") {
		t.Errorf("order diagram should contain 'Session', got %q", stripped)
	}
}

func TestRenderSessionDiagram(t *testing.T) {
	t.Parallel()

	// Step 0: just header (3 lines)
	got0 := RenderSessionDiagram(50, 0, 80)
	lines0 := strings.Split(stripANSI(got0), "\n")
	if len(lines0) > 3 {
		t.Errorf("step=0 should show at most 3 lines, got %d", len(lines0))
	}

	// Step 2: full diagram
	got2 := RenderSessionDiagram(50, 2, 80)
	stripped := stripANSI(got2)
	if !strings.Contains(stripped, "TMUX SESSION") {
		t.Errorf("session diagram should contain 'TMUX SESSION', got %q", stripped)
	}
}

func TestRenderAgentsDiagram(t *testing.T) {
	t.Parallel()

	got := RenderAgentsDiagram(200, 80)
	stripped := stripANSI(got)

	if !strings.Contains(stripped, "Claude") {
		t.Errorf("agents diagram should mention Claude, got %q", stripped)
	}
	if !strings.Contains(stripped, "Codex") {
		t.Errorf("agents diagram should mention Codex, got %q", stripped)
	}
	if !strings.Contains(stripped, "Antigravity") {
		t.Errorf("agents diagram should mention Antigravity, got %q", stripped)
	}
}

func TestRenderCommandFlowDiagram(t *testing.T) {
	t.Parallel()

	got := RenderCommandFlowDiagram(100, 0, 80)
	stripped := stripANSI(got)

	if !strings.Contains(stripped, "ntm send") {
		t.Errorf("command flow should contain 'ntm send', got %q", stripped)
	}
}

func TestRenderWorkflowDiagram(t *testing.T) {
	t.Parallel()

	got := RenderWorkflowDiagram(100, 0, 80)
	stripped := stripANSI(got)

	if !strings.Contains(stripped, "DESIGN") {
		t.Errorf("workflow diagram should contain 'DESIGN', got %q", stripped)
	}
	if !strings.Contains(stripped, "IMPLEMENT") {
		t.Errorf("workflow diagram should contain 'IMPLEMENT', got %q", stripped)
	}
	if !strings.Contains(stripped, "TEST") {
		t.Errorf("workflow diagram should contain 'TEST', got %q", stripped)
	}

	// Different activeStep changes highlighting
	got1 := RenderWorkflowDiagram(100, 1, 80)
	if got == got1 {
		t.Error("different activeStep should change output")
	}
}

func TestRenderCelebration(t *testing.T) {
	t.Parallel()

	got := RenderCelebration(10, 80)
	stripped := stripANSI(got)

	if !strings.Contains(stripped, "YOU'RE READY!") {
		t.Errorf("celebration should contain 'YOU'RE READY!', got %q", stripped)
	}
}

func TestRenderCommandCode(t *testing.T) {
	t.Parallel()

	commands := []string{
		"# Create project",
		"$ ntm quick myproject --template=go",
		"",
		"$ ntm status myproject",
	}

	// Non-typewriter mode: all visible immediately
	got := RenderCommandCode(commands, 0, false)
	stripped := stripANSI(got)

	if !strings.Contains(stripped, "Create project") {
		t.Errorf("should contain comment text, got %q", stripped)
	}
	if !strings.Contains(stripped, "ntm") {
		t.Errorf("should contain command text, got %q", stripped)
	}

	// Typewriter mode at tick=0: very little visible
	gotTw := RenderCommandCode(commands, 0, true)
	// At tick=0, visibleChars=0, almost nothing shown
	if len(stripANSI(gotTw)) > len(stripANSI(got)) {
		t.Error("typewriter at tick=0 should show less than non-typewriter")
	}

	// Typewriter mode at high tick: everything visible
	gotTwHigh := RenderCommandCode(commands, 1000, true)
	strippedHigh := stripANSI(gotTwHigh)
	if !strings.Contains(strippedHigh, "ntm") {
		t.Errorf("typewriter at high tick should show all content, got %q", strippedHigh)
	}
}

func TestRenderCommandCode_SyntaxHighlighting(t *testing.T) {
	t.Parallel()

	commands := []string{
		"$ ntm spawn myproject --cc=3 \"Build API\"",
	}

	got := RenderCommandCode(commands, 1000, false)
	// Should have ANSI codes for different parts
	if !strings.Contains(got, "\x1b[38;2;") {
		t.Error("should contain ANSI color codes for syntax highlighting")
	}
}

func TestRenderTip(t *testing.T) {
	t.Parallel()

	tip := []string{"[Tip #1] Start Small", "", "Begin with 1-2 agents.", "Scale up as needed."}

	// At high tick, all lines visible
	got := RenderTip(tip, 100, 80)
	stripped := stripANSI(got)

	if !strings.Contains(stripped, "Start Small") {
		t.Errorf("tip should contain title, got %q", stripped)
	}
	if !strings.Contains(stripped, "Begin with") {
		t.Errorf("tip should contain content, got %q", stripped)
	}

	// At low tick, only title visible
	gotLow := RenderTip(tip, 1, 80)
	strippedLow := stripANSI(gotLow)
	if !strings.Contains(strippedLow, "Start Small") {
		t.Errorf("tip at low tick should show title, got %q", strippedLow)
	}
}

func TestRenderTip_Empty(t *testing.T) {
	t.Parallel()

	got := RenderTip([]string{}, 100, 80)
	if got != "" {
		t.Errorf("empty tip should produce empty output, got %q", got)
	}
}

// =============================================================================
// model.go: Model accessors and helpers
// =============================================================================

func TestGetCurrentSlide(t *testing.T) {
	t.Parallel()

	m := New()
	if m.GetCurrentSlide() != SlideWelcome {
		t.Errorf("initial slide should be Welcome, got %v", m.GetCurrentSlide())
	}

	m2 := New(WithStartSlide(SlideComplete))
	if m2.GetCurrentSlide() != SlideComplete {
		t.Errorf("should be Complete, got %v", m2.GetCurrentSlide())
	}
}

func TestIsTransitioning(t *testing.T) {
	t.Parallel()

	m := New()
	if m.IsTransitioning() {
		t.Error("should not be transitioning initially")
	}
}

func TestEffectiveWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		width    int
		tier     layout.Tier
		expected int
	}{
		{"narrow 80", 80, layout.TierNarrow, 80},
		{"narrow 60", 60, layout.TierNarrow, 60},
		{"narrow capped at maxContent", 100, layout.TierNarrow, 90},
		{"split capped at maxContent", 150, layout.TierSplit, 90},
		{"wide uncapped to 120", 250, layout.TierWide, 120},
		{"wide narrow width", 80, layout.TierWide, 80},
		{"ultra uncapped to 140", 300, layout.TierUltra, 140},
		{"ultra narrow width", 100, layout.TierUltra, 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := New()
			m.width = tc.width
			m.tier = tc.tier
			got := m.effectiveWidth()
			if got != tc.expected {
				t.Errorf("effectiveWidth() = %d, want %d (width=%d, tier=%d)", got, tc.expected, tc.width, tc.tier)
			}
		})
	}
}

// =============================================================================
// model.go: navigation edge cases
// =============================================================================

func TestNavigation_BoundaryFirst(t *testing.T) {
	m := New(WithSkipAnimations())
	// Already at first slide, pressing left should stay
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.currentSlide != SlideWelcome {
		t.Errorf("should stay at Welcome when pressing left at start, got %v", m.currentSlide)
	}
}

func TestNavigation_HomeEnd(t *testing.T) {
	m := New(WithSkipAnimations(), WithStartSlide(SlideConcepts))

	// Home goes to first
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyHome})
	if m.currentSlide != SlideWelcome {
		t.Errorf("Home should go to Welcome, got %v", m.currentSlide)
	}

	// End goes to last
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if m.currentSlide != SlideComplete {
		t.Errorf("G should go to Complete, got %v", m.currentSlide)
	}
}

func TestNavigation_AllJumpKeys(t *testing.T) {
	expected := map[rune]SlideID{
		'1': SlideWelcome,
		'2': SlideProblem,
		'3': SlideSolution,
		'4': SlideConcepts,
		'5': SlideQuickStart,
		'6': SlideCommands,
		'7': SlideWorkflows,
		'8': SlideTips,
		'9': SlideComplete,
	}

	for key, slide := range expected {
		t.Run(string(key), func(t *testing.T) {
			m := New(WithSkipAnimations())
			m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
			if m.currentSlide != slide {
				t.Errorf("key '%c' should jump to slide %v, got %v", key, slide, m.currentSlide)
			}
		})
	}
}

func TestNavigation_RestartSlide(t *testing.T) {
	m := New()
	state := m.slideStates[m.currentSlide]
	state.localTick = 100
	state.typingDone = true
	state.revealDone = true

	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	state = m.slideStates[m.currentSlide]
	if state.localTick != 0 {
		t.Errorf("restart should reset localTick to 0, got %d", state.localTick)
	}
	if state.typingDone {
		t.Error("restart should reset typingDone")
	}
	if state.revealDone {
		t.Error("restart should reset revealDone")
	}
}

func TestNavigation_UpDown(t *testing.T) {
	m := New(WithSkipAnimations())
	state := m.slideStates[m.currentSlide]
	state.focusIndex = 0

	// Down increments
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.slideStates[m.currentSlide].focusIndex != 1 {
		t.Errorf("j should increment focusIndex, got %d", m.slideStates[m.currentSlide].focusIndex)
	}

	// Up decrements
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.slideStates[m.currentSlide].focusIndex != 0 {
		t.Errorf("k should decrement focusIndex, got %d", m.slideStates[m.currentSlide].focusIndex)
	}

	// Up at 0 stays at 0
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.slideStates[m.currentSlide].focusIndex != 0 {
		t.Errorf("up at 0 should stay at 0, got %d", m.slideStates[m.currentSlide].focusIndex)
	}
}

func TestNavigation_Tab(t *testing.T) {
	m := New(WithSkipAnimations())
	state := m.slideStates[m.currentSlide]

	if state.expanded {
		t.Error("should not be expanded initially")
	}

	m = updateModel(m, tea.KeyMsg{Type: tea.KeyTab})
	if !m.slideStates[m.currentSlide].expanded {
		t.Error("tab should toggle expanded to true")
	}

	m = updateModel(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.slideStates[m.currentSlide].expanded {
		t.Error("tab should toggle expanded back to false")
	}
}

func TestQuit(t *testing.T) {
	m := New(WithSkipAnimations())
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	if !m.quitting {
		t.Error("q should set quitting")
	}

	view := m.View()
	if view != "" {
		t.Errorf("quitting model should render empty, got %q", view)
	}
}

// =============================================================================
// model.go: window resize
// =============================================================================

func TestWindowResize(t *testing.T) {
	m := New()
	if m.width != 80 {
		t.Errorf("initial width should be 80, got %d", m.width)
	}

	m = updateModel(m, tea.WindowSizeMsg{Width: 200, Height: 50})
	if m.width != 200 {
		t.Errorf("width should be 200, got %d", m.width)
	}
	if m.height != 50 {
		t.Errorf("height should be 50, got %d", m.height)
	}
	if m.tier != layout.TierWide {
		t.Errorf("tier should be TierWide for 200px, got %v", m.tier)
	}
}

// =============================================================================
// slides.go: slide renderers produce output
// =============================================================================

func TestAllSlideRenderers(t *testing.T) {
	t.Parallel()

	m := New(WithSkipAnimations())
	m.width = 80
	m.height = 24

	slides := []struct {
		id   SlideID
		name string
	}{
		{SlideWelcome, "Welcome"},
		{SlideProblem, "Problem"},
		{SlideSolution, "Solution"},
		{SlideConcepts, "Concepts"},
		{SlideQuickStart, "QuickStart"},
		{SlideCommands, "Commands"},
		{SlideWorkflows, "Workflows"},
		{SlideTips, "Tips"},
		{SlideComplete, "Complete"},
	}

	for _, tc := range slides {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m2 := New(WithSkipAnimations(), WithStartSlide(tc.id))
			m2.width = 80
			m2.height = 24

			// Advance ticks to reveal content
			state := m2.slideStates[tc.id]
			state.localTick = 200

			var renderFn func(int) string
			switch tc.id {
			case SlideWelcome:
				renderFn = m2.renderWelcomeSlide
			case SlideProblem:
				renderFn = m2.renderProblemSlide
			case SlideSolution:
				renderFn = m2.renderSolutionSlide
			case SlideConcepts:
				renderFn = m2.renderConceptsSlide
			case SlideQuickStart:
				renderFn = m2.renderQuickStartSlide
			case SlideCommands:
				renderFn = m2.renderCommandsSlide
			case SlideWorkflows:
				renderFn = m2.renderWorkflowsSlide
			case SlideTips:
				renderFn = m2.renderTipsSlide
			case SlideComplete:
				renderFn = m2.renderCompleteSlide
			}

			got := renderFn(200)
			if got == "" {
				t.Errorf("slide %s at tick=200 should produce non-empty output", tc.name)
			}
		})
	}
}

func TestRenderNavigationBar(t *testing.T) {
	t.Parallel()

	m := New(WithSkipAnimations())
	m.width = 80
	m.animTick = 10

	got := m.renderNavigationBar()
	stripped := stripANSI(got)

	if !strings.Contains(stripped, "navigate") {
		t.Errorf("nav bar should contain 'navigate', got %q", stripped)
	}
	if !strings.Contains(stripped, "1/9") {
		t.Errorf("nav bar should show slide counter '1/9', got %q", stripped)
	}
}

func TestRenderNavigationBar_WideHints(t *testing.T) {
	t.Parallel()

	m := New(WithSkipAnimations())
	m.width = 250
	m.tier = layout.TierUltra

	got := m.renderNavigationBar()
	stripped := stripANSI(got)

	// Ultra tier should have extra hints
	if !strings.Contains(stripped, "1-9 jump") {
		t.Errorf("ultra nav bar should contain '1-9 jump', got %q", stripped)
	}
}
