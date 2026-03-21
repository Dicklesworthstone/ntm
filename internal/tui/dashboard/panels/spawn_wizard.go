// Package panels provides dashboard panel implementations.
// spawn_wizard.go provides an in-dashboard huh-powered spawn wizard overlay.
package panels

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// SpawnWizardStep tracks the current step in the multi-step wizard.
type SpawnWizardStep int

const (
	SpawnStepMethod SpawnWizardStep = iota
	SpawnStepCounts
	SpawnStepConfirm
)

// SpawnWizardResult holds the configuration produced by the wizard.
type SpawnWizardResult struct {
	CCCount   int
	CodCount  int
	GmiCount  int
	Confirmed bool
}

// SpawnWizardDoneMsg is sent when the wizard completes or is cancelled.
type SpawnWizardDoneMsg struct {
	Result    SpawnWizardResult
	Cancelled bool
}

// SpawnWizard is an in-dashboard modal overlay for configuring agent spawning.
// It uses huh forms with multi-step navigation: method -> counts -> confirm.
type SpawnWizard struct {
	width       int
	height      int
	theme       theme.Theme
	step        SpawnWizardStep
	sessionName string

	// Step 1: Method selection
	methodForm   *huh.Form
	configMethod string

	// Step 2: Agent counts
	countsForm *huh.Form
	ccStr      string
	codStr     string
	gmiStr     string

	// Step 3: Confirmation
	confirmForm *huh.Form
	confirmed   bool

	// Error state
	err error
}

// NewSpawnWizard creates a new spawn wizard overlay.
func NewSpawnWizard(sessionName string, width, height int) *SpawnWizard {
	sw := &SpawnWizard{
		width:       width,
		height:      height,
		theme:       theme.Current(),
		step:        SpawnStepMethod,
		sessionName: sessionName,
	}
	sw.initMethodForm()
	return sw
}

// SetSize updates the wizard dimensions.
func (sw *SpawnWizard) SetSize(width, height int) {
	sw.width = width
	sw.height = height
}

// Init implements tea.Model.
func (sw *SpawnWizard) Init() tea.Cmd {
	return sw.methodForm.Init()
}

// Update implements tea.Model.
func (sw *SpawnWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			// Cancel wizard
			return sw, func() tea.Msg {
				return SpawnWizardDoneMsg{Cancelled: true}
			}
		case "shift+tab":
			// Go back one step
			if sw.step > SpawnStepMethod {
				sw.step--
				sw.initCurrentForm()
				return sw, sw.currentFormInit()
			}
		}
	}

	// Update current form
	var cmd tea.Cmd
	switch sw.step {
	case SpawnStepMethod:
		form, c := sw.methodForm.Update(msg)
		sw.methodForm = form.(*huh.Form)
		cmd = c
		if sw.methodForm.State == huh.StateCompleted {
			sw.step = SpawnStepCounts
			sw.initCountsForm()
			return sw, sw.countsForm.Init()
		}
	case SpawnStepCounts:
		form, c := sw.countsForm.Update(msg)
		sw.countsForm = form.(*huh.Form)
		cmd = c
		if sw.countsForm.State == huh.StateCompleted {
			// Validate counts
			cc := parseCount(sw.ccStr)
			cod := parseCount(sw.codStr)
			gmi := parseCount(sw.gmiStr)
			if cc+cod+gmi == 0 {
				sw.err = fmt.Errorf("at least one agent is required")
				sw.initCountsForm() // Reset form
				return sw, sw.countsForm.Init()
			}
			sw.err = nil
			sw.step = SpawnStepConfirm
			sw.initConfirmForm()
			return sw, sw.confirmForm.Init()
		}
	case SpawnStepConfirm:
		form, c := sw.confirmForm.Update(msg)
		sw.confirmForm = form.(*huh.Form)
		cmd = c
		if sw.confirmForm.State == huh.StateCompleted {
			result := SpawnWizardResult{
				CCCount:   parseCount(sw.ccStr),
				CodCount:  parseCount(sw.codStr),
				GmiCount:  parseCount(sw.gmiStr),
				Confirmed: sw.confirmed,
			}
			return sw, func() tea.Msg {
				return SpawnWizardDoneMsg{
					Result:    result,
					Cancelled: !sw.confirmed,
				}
			}
		}
	}

	return sw, cmd
}

// View renders the wizard overlay.
func (sw *SpawnWizard) View() string {
	t := sw.theme

	// Calculate modal dimensions
	modalWidth := sw.width - 20
	if modalWidth > 70 {
		modalWidth = 70
	}
	if modalWidth < 40 {
		modalWidth = 40
	}

	modalHeight := sw.height - 10
	if modalHeight > 20 {
		modalHeight = 20
	}
	if modalHeight < 12 {
		modalHeight = 12
	}

	// Build content
	var content strings.Builder

	// Title
	titleStyle := lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true)
	content.WriteString(titleStyle.Render("Spawn Wizard") + "\n")
	content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render(
		fmt.Sprintf("Session: %s", sw.sessionName)) + "\n\n")

	// Step indicator
	stepStyle := lipgloss.NewStyle().Foreground(t.Overlay)
	steps := []string{"Method", "Counts", "Confirm"}
	var stepIndicators []string
	for i, s := range steps {
		if SpawnWizardStep(i) == sw.step {
			stepIndicators = append(stepIndicators, lipgloss.NewStyle().
				Foreground(t.Primary).Bold(true).Render("● "+s))
		} else if SpawnWizardStep(i) < sw.step {
			stepIndicators = append(stepIndicators, lipgloss.NewStyle().
				Foreground(t.Success).Render("✓ "+s))
		} else {
			stepIndicators = append(stepIndicators, stepStyle.Render("○ "+s))
		}
	}
	content.WriteString(strings.Join(stepIndicators, "  →  ") + "\n\n")

	// Error message if any
	if sw.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(t.Error).
			Bold(true)
		content.WriteString(errorStyle.Render("⚠ "+sw.err.Error()) + "\n\n")
	}

	// Current form
	switch sw.step {
	case SpawnStepMethod:
		content.WriteString(sw.methodForm.View())
	case SpawnStepCounts:
		content.WriteString(sw.countsForm.View())
	case SpawnStepConfirm:
		content.WriteString(sw.confirmForm.View())
	}

	// Hint
	content.WriteString("\n")
	hintStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
	if sw.step > SpawnStepMethod {
		content.WriteString(hintStyle.Render("Shift+Tab: previous step • Esc: cancel"))
	} else {
		content.WriteString(hintStyle.Render("Esc: cancel"))
	}

	// Modal box
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Background(t.Base).
		Padding(1, 2).
		Width(modalWidth).
		Height(modalHeight)

	return boxStyle.Render(content.String())
}

// Keybindings returns the wizard keybindings for help display.
func (sw *SpawnWizard) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select/confirm")),
			Description: "Select option or confirm",
			Action:      "select",
		},
		{
			Key:         key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back")),
			Description: "Go to previous step",
			Action:      "back",
		},
		{
			Key:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
			Description: "Cancel wizard",
			Action:      "cancel",
		},
	}
}

func (sw *SpawnWizard) initMethodForm() {
	sw.methodForm = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Configuration method").
				Description("How would you like to configure agents?").
				Options(
					huh.NewOption("Manual — pick agent types and counts", "manual"),
					huh.NewOption("Quick — balanced mix (2 CC, 1 Cod, 1 Gmi)", "quick"),
					huh.NewOption("Minimal — single Claude agent", "minimal"),
				).
				Value(&sw.configMethod),
		),
	).WithTheme(theme.HuhTheme()).WithWidth(sw.modalContentWidth())
}

func (sw *SpawnWizard) initCountsForm() {
	// Pre-fill based on method
	switch sw.configMethod {
	case "quick":
		sw.ccStr = "2"
		sw.codStr = "1"
		sw.gmiStr = "1"
	case "minimal":
		sw.ccStr = "1"
		sw.codStr = "0"
		sw.gmiStr = "0"
	default:
		// Manual - keep existing values or defaults
		if sw.ccStr == "" {
			sw.ccStr = "0"
		}
		if sw.codStr == "" {
			sw.codStr = "0"
		}
		if sw.gmiStr == "" {
			sw.gmiStr = "0"
		}
	}

	sw.countsForm = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Claude agents (cc)").
				Description("Number of Claude Code agents").
				Placeholder("0").
				Value(&sw.ccStr).
				Validate(validateAgentCount),
			huh.NewInput().
				Title("Codex agents (cod)").
				Description("Number of OpenAI Codex agents").
				Placeholder("0").
				Value(&sw.codStr).
				Validate(validateAgentCount),
			huh.NewInput().
				Title("Gemini agents (gmi)").
				Description("Number of Google Gemini agents").
				Placeholder("0").
				Value(&sw.gmiStr).
				Validate(validateAgentCount),
		),
	).WithTheme(theme.HuhTheme()).WithWidth(sw.modalContentWidth())
}

func (sw *SpawnWizard) initConfirmForm() {
	cc := parseCount(sw.ccStr)
	cod := parseCount(sw.codStr)
	gmi := parseCount(sw.gmiStr)
	total := cc + cod + gmi

	summary := fmt.Sprintf("Spawn %d agent(s):\n  Claude: %d, Codex: %d, Gemini: %d",
		total, cc, cod, gmi)

	sw.confirmForm = huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Confirm spawn").
				Description(summary).
				Affirmative("Spawn").
				Negative("Cancel").
				Value(&sw.confirmed),
		),
	).WithTheme(theme.HuhTheme()).WithWidth(sw.modalContentWidth())
}

func (sw *SpawnWizard) initCurrentForm() {
	switch sw.step {
	case SpawnStepMethod:
		sw.initMethodForm()
	case SpawnStepCounts:
		sw.initCountsForm()
	case SpawnStepConfirm:
		sw.initConfirmForm()
	}
}

func (sw *SpawnWizard) currentFormInit() tea.Cmd {
	switch sw.step {
	case SpawnStepMethod:
		return sw.methodForm.Init()
	case SpawnStepCounts:
		return sw.countsForm.Init()
	case SpawnStepConfirm:
		return sw.confirmForm.Init()
	}
	return nil
}

func (sw *SpawnWizard) modalContentWidth() int {
	w := sw.width - 26 // Account for padding and borders
	if w > 60 {
		w = 60
	}
	if w < 30 {
		w = 30
	}
	return w
}

// validateAgentCount ensures the input is a valid non-negative integer.
func validateAgentCount(s string) error {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("must be a number")
	}
	if n < 0 {
		return fmt.Errorf("must be non-negative")
	}
	if n > 10 {
		return fmt.Errorf("max 10 per type")
	}
	return nil
}

// parseCount safely parses a count string, returning 0 on error.
func parseCount(s string) int {
	if s == "" {
		return 0
	}
	n, _ := strconv.Atoi(s)
	if n < 0 {
		return 0
	}
	return n
}
