package tutorial

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

func TestAbs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"zero", 0, 0},
		{"positive", 42, 42},
		{"negative", -42, 42},
		{"one", 1, 1},
		{"negative one", -1, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := abs(tc.input)
			if got != tc.want {
				t.Errorf("abs(%d) = %d, want %d", tc.input, got, tc.want)
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
