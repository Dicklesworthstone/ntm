// Package tutorial provides an interactive, visually stunning tutorial for NTM
package tutorial

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// TickMsg is sent on each animation frame
type TickMsg time.Time

// TransitionDoneMsg signals a transition has completed
type TransitionDoneMsg struct{}

// SlideID identifies each slide in the tutorial
type SlideID int

const (
	SlideWelcome SlideID = iota
	SlideProblem
	SlideSolution
	SlideConcepts
	SlideQuickStart
	SlideCommands
	SlideWorkflows
	SlideTips
	SlideComplete
)

// SlideCount is the total number of slides
const SlideCount = 9

// TransitionType defines how slides transition
type TransitionType int

const (
	TransitionNone TransitionType = iota
	TransitionFadeOut
	TransitionFadeIn
	TransitionSlideLeft
	TransitionSlideRight
	TransitionZoomIn
	TransitionZoomOut
	TransitionDissolve
)

// Model is the main tutorial Bubble Tea model
type Model struct {
	// Dimensions
	width  int
	height int

	// Current state
	currentSlide SlideID
	animTick     int

	// Transition state
	transitioning  bool
	transitionType TransitionType
	transitionTick int
	nextSlide      SlideID

	// Slide-specific state
	slideStates map[SlideID]*SlideState

	// Visual settings
	theme  theme.Theme
	styles theme.Styles
	icons  icons.IconSet
	tier   layout.Tier

	// Options
	skipAnimations bool
	quitting       bool

	// Particle system for celebrations
	particles []Particle
}

// SlideState holds per-slide animation state
type SlideState struct {
	// Typing animation
	typingIndex   int
	typingDone    bool
	typingContent []string

	// Reveal animation
	revealIndex int
	revealDone  bool

	// Interactive state
	focusIndex    int
	selectedItems []int
	expanded      bool

	// Diagram animation
	diagramStep int
	diagramDone bool

	// Custom tick for slide-specific animations
	localTick int
}

// New creates a new tutorial model
func New(opts ...Option) Model {
	t := theme.Current()
	m := Model{
		width:        80,
		height:       24,
		tier:         layout.TierForWidth(80),
		currentSlide: SlideWelcome,
		slideStates:  make(map[SlideID]*SlideState),
		theme:        t,
		styles:       theme.NewStyles(t),
		icons:        icons.Current(),
		particles:    make([]Particle, 0),
	}

	// Initialize slide states
	for i := SlideID(0); i < SlideID(SlideCount); i++ {
		m.slideStates[i] = &SlideState{}
	}

	// Apply options
	for _, opt := range opts {
		opt(&m)
	}

	return m
}

// Option configures the tutorial model
type Option func(*Model)

// WithSkipAnimations disables animations for faster navigation
func WithSkipAnimations() Option {
	return func(m *Model) {
		m.skipAnimations = true
	}
}

// WithStartSlide sets the starting slide
func WithStartSlide(slide SlideID) Option {
	return func(m *Model) {
		m.currentSlide = slide
	}
}

// Init implements tea.Model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.tick(),
		tea.EnterAltScreen,
	)
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Millisecond*33, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// Update implements tea.Model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.tier = layout.TierForWidth(msg.Width)
		return m, nil

	case TickMsg:
		m.animTick++

		// Update current slide's local tick
		if state, ok := m.slideStates[m.currentSlide]; ok {
			state.localTick++
		}

		// Update transition
		if m.transitioning {
			m.transitionTick++
			if m.transitionTick >= transitionDuration(m.transitionType) {
				m.transitioning = false
				m.currentSlide = m.nextSlide
				m.transitionTick = 0
				// Reset next slide's state for fresh animations
				if state, ok := m.slideStates[m.currentSlide]; ok {
					state.localTick = 0
					state.typingIndex = 0
					state.typingDone = false
					state.revealIndex = 0
					state.revealDone = false
					state.diagramStep = 0
					state.diagramDone = false
				}
			}
		}

		// Update typing animations
		m.updateTyping()

		// Update particles
		m.updateParticles()

		return m, m.tick()

	case TransitionDoneMsg:
		m.transitioning = false
		m.currentSlide = m.nextSlide
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keys
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "?":
		// Toggle help
		return m, nil
	}

	// If transitioning, ignore navigation
	if m.transitioning {
		return m, nil
	}

	switch msg.String() {
	case "right", "l", "n", " ", "enter":
		// Next slide
		if m.currentSlide < SlideID(SlideCount-1) {
			m.goToSlide(m.currentSlide+1, TransitionSlideLeft)
		} else {
			// On last slide, quit
			m.quitting = true
			return m, tea.Quit
		}

	case "left", "h", "p", "backspace":
		// Previous slide
		if m.currentSlide > 0 {
			m.goToSlide(m.currentSlide-1, TransitionSlideRight)
		}

	case "home", "g":
		// First slide
		if m.currentSlide != SlideWelcome {
			m.goToSlide(SlideWelcome, TransitionSlideRight)
		}

	case "end", "G":
		// Last slide
		if m.currentSlide != SlideComplete {
			m.goToSlide(SlideComplete, TransitionSlideLeft)
		}

	case "1":
		m.goToSlide(SlideWelcome, TransitionDissolve)
	case "2":
		m.goToSlide(SlideProblem, TransitionDissolve)
	case "3":
		m.goToSlide(SlideSolution, TransitionDissolve)
	case "4":
		m.goToSlide(SlideConcepts, TransitionDissolve)
	case "5":
		m.goToSlide(SlideQuickStart, TransitionDissolve)
	case "6":
		m.goToSlide(SlideCommands, TransitionDissolve)
	case "7":
		m.goToSlide(SlideWorkflows, TransitionDissolve)
	case "8":
		m.goToSlide(SlideTips, TransitionDissolve)
	case "9":
		m.goToSlide(SlideComplete, TransitionDissolve)

	case "s":
		// Skip current animation
		m.skipCurrentAnimation()

	case "r":
		// Restart current slide animation
		m.restartSlideAnimation()

	// Slide-specific keys handled in slide views
	case "up", "k":
		if state, ok := m.slideStates[m.currentSlide]; ok {
			if state.focusIndex > 0 {
				state.focusIndex--
			}
		}

	case "down", "j":
		if state, ok := m.slideStates[m.currentSlide]; ok {
			state.focusIndex++
		}

	case "tab":
		if state, ok := m.slideStates[m.currentSlide]; ok {
			state.expanded = !state.expanded
		}
	}

	return m, nil
}

func (m *Model) goToSlide(slide SlideID, _ TransitionType) {
	// Always do instant transitions - slide effects look janky
	m.currentSlide = slide
	// Reset slide state for fresh animations
	if state, ok := m.slideStates[slide]; ok {
		state.localTick = 0
		state.typingIndex = 0
		state.typingDone = false
		state.revealIndex = 0
		state.revealDone = false
		state.diagramStep = 0
		state.diagramDone = false
	}
}

func (m *Model) updateTyping() {
	if m.skipAnimations {
		return
	}

	state, ok := m.slideStates[m.currentSlide]
	if !ok || state.typingDone {
		return
	}

	// Advance typing every few ticks
	if m.animTick%2 == 0 && len(state.typingContent) > 0 {
		totalChars := 0
		for _, line := range state.typingContent {
			totalChars += len(line)
		}

		if state.typingIndex < totalChars {
			state.typingIndex++
		} else {
			state.typingDone = true
		}
	}
}

func (m *Model) updateParticles() {
	// Update existing particles
	alive := make([]Particle, 0)
	for _, p := range m.particles {
		p.Update()
		if p.Life > 0 {
			alive = append(alive, p)
		}
	}
	m.particles = alive
}

func (m *Model) skipCurrentAnimation() {
	state, ok := m.slideStates[m.currentSlide]
	if !ok {
		return
	}

	state.typingDone = true
	state.revealDone = true
	state.diagramDone = true
	if len(state.typingContent) > 0 {
		totalChars := 0
		for _, line := range state.typingContent {
			totalChars += len(line)
		}
		state.typingIndex = totalChars
	}
}

func (m *Model) restartSlideAnimation() {
	state, ok := m.slideStates[m.currentSlide]
	if !ok {
		return
	}

	state.localTick = 0
	state.typingIndex = 0
	state.typingDone = false
	state.revealIndex = 0
	state.revealDone = false
	state.diagramStep = 0
	state.diagramDone = false
}

func (m *Model) spawnParticles(x, y int, count int, ptype ParticleType) {
	for i := 0; i < count; i++ {
		m.particles = append(m.particles, NewParticle(x, y, ptype))
	}
}

func transitionDuration(t TransitionType) int {
	switch t {
	case TransitionFadeOut, TransitionFadeIn:
		return 15
	case TransitionSlideLeft, TransitionSlideRight:
		return 12
	case TransitionZoomIn, TransitionZoomOut:
		return 18
	case TransitionDissolve:
		return 10
	default:
		return 0
	}
}

// View implements tea.Model
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	return m.renderSlide()
}

// GetCurrentSlide returns the current slide ID
func (m Model) GetCurrentSlide() SlideID {
	return m.currentSlide
}

// IsTransitioning returns whether a transition is in progress
func (m Model) IsTransitioning() bool {
	return m.transitioning
}
