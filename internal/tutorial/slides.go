package tutorial

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
)

// maxContentWidth is the maximum width for tutorial content to maintain readability
const maxContentWidth = 90

// effectiveWidth returns the content width, constrained to maxContentWidth on wide screens
func (m Model) effectiveWidth() int {
	maxWidth := maxContentWidth
	if m.tier >= layout.TierUltra {
		maxWidth = 140
	} else if m.tier >= layout.TierWide {
		maxWidth = 120
	}

	if m.width > maxWidth {
		return maxWidth
	}
	return m.width
}

// renderSlide renders the current slide
func (m Model) renderSlide() string {
	state := m.slideStates[m.currentSlide]
	tick := state.localTick
	if m.skipAnimations {
		// Render the fully revealed frame immediately when motion is disabled.
		tick = 1000
	}

	var content string

	switch m.currentSlide {
	case SlideWelcome:
		content = m.renderWelcomeSlide(tick)
	case SlideProblem:
		content = m.renderProblemSlide(tick)
	case SlideSolution:
		content = m.renderSolutionSlide(tick)
	case SlideConcepts:
		content = m.renderConceptsSlide(tick)
	case SlideQuickStart:
		content = m.renderQuickStartSlide(tick)
	case SlideCommands:
		content = m.renderCommandsSlide(tick)
	case SlideWorkflows:
		content = m.renderWorkflowsSlide(tick)
	case SlideTips:
		content = m.renderTipsSlide(tick)
	case SlideComplete:
		content = m.renderCompleteSlide(tick)
	}

	// Calculate effective width for main content
	effWidth := m.effectiveWidth()

	// On wide screens, center the content within a constrained width
	// If we have a side panel, we center within the remaining space minus side panel
	centerWidth := m.width
	if m.tier >= layout.TierWide {
		// Reserve space for side panel (approx 35 cols)
		centerWidth -= 35
		if centerWidth < effWidth {
			centerWidth = effWidth
		}
	}

	if centerWidth > effWidth {
		padding := (centerWidth - effWidth) / 2
		lines := strings.Split(content, "\n")
		var centered strings.Builder
		for i, line := range lines {
			if i > 0 {
				centered.WriteString("\n")
			}
			centered.WriteString(strings.Repeat(" ", padding))
			centered.WriteString(line)
		}
		content = centered.String()
	}

	// Add side panel on wide screens
	if m.tier >= layout.TierWide {
		sidePanel := m.renderSidePanel()
		content = lipgloss.JoinHorizontal(lipgloss.Top, content, sidePanel)
	}

	// Add navigation bar at bottom
	content += "\n" + m.renderNavigationBar()

	return content
}

// renderSidePanel renders the persistent side panel for wide screens
func (m Model) renderSidePanel() string {
	t := m.theme
	width := 30

	style := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true). // Left border
		BorderForeground(t.Surface1).
		Padding(0, 1).
		Width(width).
		Height(m.height - 3) // Leave room for nav bar

	var b strings.Builder
	b.WriteString(styles.GradientText("  QUICK REF", "#89b4fa", "#cba6f7") + "\n\n")

	// Key bindings
	keys := []struct{ k, d string }{
		{"→/SPACE", "Next"},
		{"←/BACK", "Prev"},
		{"s", "Skip Anim"},
		{"r", "Replay"},
		{"q", "Quit"},
	}

	for _, k := range keys {
		keyStyle := lipgloss.NewStyle().Foreground(t.Text).Bold(true)
		descStyle := lipgloss.NewStyle().Foreground(t.Overlay)
		b.WriteString(fmt.Sprintf("  %-10s %s\n", keyStyle.Render(k.k), descStyle.Render(k.d)))
	}

	// Progress Map
	b.WriteString("\n" + styles.GradientText("  PROGRESS", "#a6e3a1", "#94e2d5") + "\n\n")

	slides := []string{
		"Welcome",
		"Problem",
		"Solution",
		"Concepts",
		"Quick Start",
		"Commands",
		"Workflows",
		"Tips",
		"Complete",
	}

	for i, name := range slides {
		var marker string
		var itemStyle lipgloss.Style

		if SlideID(i) == m.currentSlide {
			marker = "●"
			itemStyle = lipgloss.NewStyle().Foreground(t.Pink).Bold(true)
		} else if SlideID(i) < m.currentSlide {
			marker = "✓"
			itemStyle = lipgloss.NewStyle().Foreground(t.Green)
		} else {
			marker = "○"
			itemStyle = lipgloss.NewStyle().Foreground(t.Surface2)
		}

		b.WriteString(fmt.Sprintf("  %s %s\n", itemStyle.Render(marker), itemStyle.Render(name)))
	}

	return style.Render(b.String())
}

// renderWelcomeSlide renders the animated welcome slide
func (m Model) renderWelcomeSlide(tick int) string {
	var b strings.Builder

	// Add some top padding
	topPad := (m.height - 20) / 3
	if topPad < 2 {
		topPad = 2
	}
	b.WriteString(strings.Repeat("\n", topPad))

	// Animated logo
	logo := RenderAnimatedLogo(tick, m.effectiveWidth())
	b.WriteString(logo)

	// Sparkle effects around logo after reveal
	if tick > 40 {
		b.WriteString("\n\n")
		sparkleText := "Press SPACE or → to begin your journey"
		sparkled := WaveText(sparkleText, tick, 1.0, []string{"#89b4fa", "#cba6f7", "#f5c2e7"})
		b.WriteString(centerTextWidth(sparkled, m.effectiveWidth()))
	}

	return b.String()
}

// renderProblemSlide shows the chaos of unorganized terminals
func (m Model) renderProblemSlide(tick int) string {
	var b strings.Builder

	// Title with dramatic reveal
	title := "The Problem"
	if tick > 0 {
		titleStyled := styles.GradientText(title, "#f38ba8", "#fab387")
		b.WriteString("\n")
		b.WriteString(centerTextWidth(titleStyled, m.effectiveWidth()))
		b.WriteString("\n\n")
	}

	// Problem descriptions with staggered reveal
	problems := []string{
		"- Window chaos — each agent needs its own terminal",
		"- Context switching — jumping between windows breaks flow",
		"- No orchestration — manual copy-paste to multiple agents",
		"- Session fragility — disconnect and lose everything",
		"- Setup friction — manual creation for every project",
	}

	if tick > 10 {
		for i, problem := range problems {
			revealTick := tick - 10 - i*8
			if revealTick > 0 {
				colored := styles.GradientText(problem, "#f38ba8", "#fab387")
				b.WriteString("    " + colored + "\n")
			}
		}
	}

	// Chaos diagram
	if tick > 60 {
		b.WriteString("\n")
		chaos := RenderAnimatedChaosDiagram(tick-60, m.effectiveWidth())
		b.WriteString(chaos)
	}

	// Red warning glow
	if tick > 80 {
		b.WriteString("\n\n")
		warning := PulseText("This is not sustainable...", tick, "#f38ba8")
		b.WriteString(centerTextWidth(warning, m.effectiveWidth()))
	}

	return b.String()
}

// renderSolutionSlide shows the organized NTM approach
func (m Model) renderSolutionSlide(tick int) string {
	var b strings.Builder

	// Title with triumphant reveal
	title := "The Solution"
	if tick > 0 {
		titleStyled := styles.Shimmer(title, tick, "#a6e3a1", "#94e2d5", "#89dceb")
		b.WriteString("\n")
		b.WriteString(centerTextWidth(titleStyled, m.effectiveWidth()))
		b.WriteString("\n\n")
	}

	// Solution points with staggered reveal
	solutions := []string{
		"- One session, many agents — all agents in organized tmux panes",
		"- Named panes — easy identification (myproject__cc_1)",
		"- Broadcast prompts — send to all agents with one command",
		"- Persistent sessions — detach/reattach without losing state",
		"- Quick setup — create project + agents in seconds",
		"- Beautiful TUI — Catppuccin-themed command palette",
	}

	if tick > 10 {
		for i, solution := range solutions {
			revealTick := tick - 10 - i*8
			if revealTick > 0 {
				// Grow effect (use runes to properly handle emoji)
				if revealTick < 5 {
					runes := []rune(solution)
					visibleLen := revealTick * len(runes) / 5
					solution = string(runes[:visibleLen])
				}

				colored := styles.GradientText(solution, "#a6e3a1", "#89dceb")
				b.WriteString("    " + colored + "\n")
			}
		}
	}

	// Order diagram
	if tick > 70 {
		b.WriteString("\n")
		order := RenderAnimatedOrderDiagram(tick-70, m.effectiveWidth())
		b.WriteString(order)
	}

	// Success message
	if tick > 100 {
		b.WriteString("\n\n")
		success := styles.Shimmer("Welcome to your multi-agent command center!", tick, "#a6e3a1", "#89b4fa", "#cba6f7")
		b.WriteString(centerTextWidth(success, m.effectiveWidth()))
	}

	return b.String()
}

// renderConceptsSlide explains core concepts with diagrams
func (m Model) renderConceptsSlide(tick int) string {
	var b strings.Builder

	// Title
	title := "Core Concepts"
	titleStyled := styles.Shimmer(title, tick, "#89b4fa", "#b4befe", "#cba6f7")
	b.WriteString("\n")
	b.WriteString(centerTextWidth(titleStyled, m.effectiveWidth()))
	b.WriteString("\n\n")

	// Three concepts to explain
	concepts := []struct {
		icon  string
		name  string
		desc  string
		color string
		delay int
	}{
		{"[]", "SESSION", "A tmux container for all your work", "#89b4fa", 0},
		{"AI", "AGENTS", "AI assistants (Claude, Codex, Antigravity)", "#cba6f7", 25},
		{"|>", "PANES", "Individual terminals within a session", "#a6e3a1", 50},
	}

	// Render concept cards
	if tick > 10 {
		for _, c := range concepts {
			revealTick := tick - 10 - c.delay
			if revealTick <= 0 {
				continue
			}

			// Concept header
			header := fmt.Sprintf("%s  %s", c.icon, c.name)
			if revealTick < 10 {
				header = styles.GradientText(header, c.color, "#1e1e2e")
			} else {
				header = styles.Shimmer(header, tick, c.color, "#f5c2e7")
			}
			b.WriteString("      " + header + "\n")

			// Description
			if revealTick > 5 {
				desc := styles.GradientText("      "+c.desc, "#a6adc8", "#6c7086")
				b.WriteString(desc + "\n\n")
			}
		}
	}

	// Session diagram
	if tick > 90 {
		diagramStep := (tick - 90) / 30
		diagram := RenderSessionDiagram(tick-90, diagramStep, m.effectiveWidth())
		b.WriteString(diagram)
	}

	// Agents explanation
	if tick > 150 {
		agents := RenderAgentsDiagram(tick-150, m.effectiveWidth())
		b.WriteString("\n")
		b.WriteString(agents)
	}

	return b.String()
}

// renderQuickStartSlide shows commands to get started
func (m Model) renderQuickStartSlide(tick int) string {
	var b strings.Builder

	// Title
	title := "Quick Start"
	titleStyled := styles.Shimmer(title, tick, "#a6e3a1", "#94e2d5", "#89dceb")
	b.WriteString("\n")
	b.WriteString(centerTextWidth(titleStyled, m.effectiveWidth()))
	b.WriteString("\n\n")

	// Step 1: Create project
	if tick > 10 {
		step1 := styles.GradientText("  Step 1: Create a project", "#89b4fa", "#74c7ec")
		b.WriteString(step1 + "\n\n")

		code1 := []string{
			"$ ntm quick myproject --template=go",
		}
		b.WriteString("      " + RenderCommandCode(code1, tick-10, true) + "\n\n")
	}

	// Step 2: Spawn agents
	if tick > 50 {
		step2 := styles.GradientText("  Step 2: Spawn agents", "#cba6f7", "#f5c2e7")
		b.WriteString(step2 + "\n\n")

		code2 := []string{
			"$ ntm spawn myproject --cc=3 --cod=2 --agy=1",
		}
		b.WriteString("      " + RenderCommandCode(code2, tick-50, true) + "\n\n")
	}

	// Step 3: Send prompts
	if tick > 90 {
		step3 := styles.GradientText("  Step 3: Send prompts", "#a6e3a1", "#94e2d5")
		b.WriteString(step3 + "\n\n")

		code3 := []string{
			"$ ntm send myproject --all \"Build a REST API\"",
		}
		b.WriteString("      " + RenderCommandCode(code3, tick-90, true) + "\n\n")
	}

	// Command flow diagram
	if tick > 130 {
		flow := RenderCommandFlowDiagram(tick-130, 0, m.effectiveWidth())
		b.WriteString(flow)
	}

	// Success message
	if tick > 170 {
		success := styles.Shimmer("That's it! You're orchestrating AI agents!", tick, "#a6e3a1", "#f9e2af", "#f5c2e7")
		b.WriteString("\n")
		b.WriteString(centerTextWidth(success, m.effectiveWidth()))
	}

	return b.String()
}

// renderCommandsSlide shows the command reference
func (m Model) renderCommandsSlide(tick int) string {
	var b strings.Builder

	// Title
	title := "Command Reference"
	titleStyled := styles.Shimmer(title, tick, "#89b4fa", "#b4befe", "#cba6f7")
	b.WriteString("\n")
	b.WriteString(centerTextWidth(titleStyled, m.effectiveWidth()))
	b.WriteString("\n\n")

	// Command categories
	categories := []struct {
		name     string
		color    string
		commands [][]string // [command, alias, description]
	}{
		{
			name:  "Session Creation",
			color: "#89b4fa",
			commands: [][]string{
				{"ntm create", "cnt", "Create empty session"},
				{"ntm spawn", "sat", "Create + launch agents"},
				{"ntm quick", "qps", "Full project setup"},
			},
		},
		{
			name:  "Agent Management",
			color: "#cba6f7",
			commands: [][]string{
				{"ntm add", "ant", "Add more agents"},
				{"ntm send", "bp", "Broadcast prompt"},
				{"ntm interrupt", "int", "Stop all agents"},
			},
		},
		{
			name:  "Navigation",
			color: "#a6e3a1",
			commands: [][]string{
				{"ntm attach", "rnt", "Reattach to session"},
				{"ntm status", "snt", "Show session status"},
				{"ntm view", "vnt", "Tile and attach"},
			},
		},
		{
			name:  "Utilities",
			color: "#f9e2af",
			commands: [][]string{
				{"ntm palette", "ncp", "Command palette"},
				{"ntm copy", "cpnt", "Copy pane output (pane, filters, clipboard/file)"},
				{"ntm save", "svnt", "Save outputs to files"},
			},
		},
	}

	delay := 0
	for _, cat := range categories {
		if tick <= delay {
			break
		}

		// Category header
		header := styles.GradientText("  "+cat.name, cat.color, "#cdd6f4")
		b.WriteString(header + "\n")

		// Commands
		for i, cmd := range cat.commands {
			cmdDelay := delay + 5 + i*3
			if tick <= cmdDelay {
				break
			}

			// Format: command (alias) - description
			cmdText := fmt.Sprintf("    %-15s", cmd[0])
			aliasText := fmt.Sprintf("%-6s", cmd[1])
			descText := cmd[2]

			cmdStyled := styles.GradientText(cmdText, cat.color, "#6c7086")
			aliasStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#45475a")).Render("[" + aliasText + "]")
			descStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6adc8")).Render(descText)

			b.WriteString(cmdStyled + " " + aliasStyled + " " + descStyled + "\n")
		}

		b.WriteString("\n")
		delay += 25
	}

	// Tip
	if tick > 120 {
		tip := styles.GradientText("  Tip: Use `ntm --help` for full command details", "#6c7086", "#45475a")
		b.WriteString("\n" + tip)
	}

	return b.String()
}

// renderWorkflowsSlide shows multi-agent strategies
func (m Model) renderWorkflowsSlide(tick int) string {
	var b strings.Builder

	// Title
	title := "Multi-Agent Workflows"
	titleStyled := styles.Shimmer(title, tick, "#89b4fa", "#cba6f7", "#f5c2e7")
	b.WriteString("\n")
	b.WriteString(centerTextWidth(titleStyled, m.effectiveWidth()))
	b.WriteString("\n\n")

	// Workflow strategies
	workflows := []struct {
		name  string
		icon  string
		desc  string
		color string
	}{
		{"Divide & Conquer", "*", "Claude designs, Codex implements, Antigravity tests", "#89b4fa"},
		{"Competitive", "*", "Same task to all agents, compare results", "#a6e3a1"},
		{"Specialist Teams", "*", "Assign roles: architect, coder, reviewer", "#cba6f7"},
		{"Review Pipeline", "*", "Agent 1 codes, Agent 2 reviews, Agent 3 tests", "#f9e2af"},
	}

	for i, wf := range workflows {
		revealTick := tick - 10 - i*20
		if revealTick <= 0 {
			continue
		}

		// Workflow card
		header := fmt.Sprintf("  %s  %s", wf.icon, wf.name)
		headerStyled := styles.GradientText(header, wf.color, "#cdd6f4")
		b.WriteString(headerStyled + "\n")

		if revealTick > 5 {
			desc := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6adc8")).Render("      " + wf.desc)
			b.WriteString(desc + "\n\n")
		}
	}

	// Animated workflow diagram
	if tick > 100 {
		activeStep := (tick - 100) / 40 % 4
		diagram := RenderWorkflowDiagram(tick-100, activeStep, m.effectiveWidth())
		b.WriteString(diagram)
	}

	return b.String()
}

// renderTipsSlide shows pro tips
func (m Model) renderTipsSlide(tick int) string {
	var b strings.Builder

	// Title
	title := "Pro Tips"
	titleStyled := styles.Shimmer(title, tick, "#f9e2af", "#fab387", "#f5c2e7")
	b.WriteString("\n")
	b.WriteString(centerTextWidth(titleStyled, m.effectiveWidth()))
	b.WriteString("\n\n")

	// Tips with staggered reveal
	tips := []struct {
		icon   string
		tip    string
		detail string
		color  string
	}{
		{"*", "Start Small", "Begin with 1-2 agents, scale with `ntm add`", "#89b4fa"},
		{"*", "Use Aliases", "Type `bp` instead of `ntm send` for speed", "#cba6f7"},
		{"*", "F6 Palette", "Press F6 in tmux for instant command palette", "#f5c2e7"},
		{"*", "Save Often", "`ntm save myproject -o ~/logs` preserves outputs", "#a6e3a1"},
		{"*", "Width Tiers", "120+ cols split view; 200+/240+ unlock richer metadata", "#94e2d5"},
		{"*", "Icons", "ASCII by default; use NTM_ICONS=unicode|nerd only with good fonts", "#f2cdcd"},
		{"*", "Tmux Keys", "Ctrl+A, D to detach • Ctrl+A, [ to scroll", "#f9e2af"},
		{"*", "Interrupt Fast", "`ntm interrupt myproject` stops all agents", "#f38ba8"},
	}

	for i, t := range tips {
		revealTick := tick - 10 - i*15
		if revealTick <= 0 {
			continue
		}

		// Tip card with animation
		header := fmt.Sprintf("  %s  %s", t.icon, t.tip)
		if revealTick < 10 {
			header = styles.GradientText(header, t.color, "#1e1e2e")
		} else {
			header = styles.GradientText(header, t.color, "#cdd6f4")
		}
		b.WriteString(header + "\n")

		if revealTick > 5 {
			detail := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Render("      " + t.detail)
			b.WriteString(detail + "\n\n")
		}
	}

	return b.String()
}

// renderCompleteSlide shows the celebration and next steps
func (m Model) renderCompleteSlide(tick int) string {
	var b strings.Builder

	// Add top padding
	topPad := (m.height - 18) / 3
	if topPad < 2 {
		topPad = 2
	}
	b.WriteString(strings.Repeat("\n", topPad))

	// Celebration particles
	if len(m.particles) > 0 {
		var particleRow strings.Builder
		for _, p := range m.particles {
			if rendered := p.Render(); rendered != "" {
				if particleRow.Len() > 0 {
					particleRow.WriteString("  ")
				}
				particleRow.WriteString(rendered)
			}
		}
		if particleRow.Len() > 0 {
			b.WriteString(centerTextWidth(particleRow.String(), m.effectiveWidth()) + "\n")
		}
	}

	// Celebration banner
	if tick > 0 {
		celebration := RenderCelebration(tick, m.effectiveWidth())
		b.WriteString(celebration)
	}

	// Success message
	if tick > 20 {
		msg := "You've completed the NTM tutorial!"
		msgStyled := styles.Shimmer(msg, tick, "#a6e3a1", "#89dceb", "#89b4fa")
		b.WriteString("\n")
		b.WriteString(centerTextWidth(msgStyled, m.effectiveWidth()))
	}

	// Next steps
	if tick > 40 {
		b.WriteString("\n\n")
		nextTitle := styles.GradientText("  Next Steps", "#89b4fa", "#74c7ec")
		b.WriteString(centerTextWidth(nextTitle, m.effectiveWidth()) + "\n\n")

		steps := []string{
			"1. Run `ntm deps -v` to verify your setup",
			"2. Create your first project with `ntm quick myproject`",
			"3. Spawn some agents with `ntm spawn myproject --cc=2`",
			"4. Open the palette with `ntm palette myproject`",
		}

		for i, step := range steps {
			stepDelay := 40 + i*10
			if tick > stepDelay {
				stepStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4")).Render("      " + step)
				b.WriteString(stepStyled + "\n")
			}
		}
	}

	// Resources
	if tick > 100 {
		b.WriteString("\n")
		resources := styles.GradientText("  Need help? Run `ntm --help` or visit the docs", "#6c7086", "#45475a")
		b.WriteString(centerTextWidth(resources, m.effectiveWidth()))
	}

	// Exit hint
	if tick > 120 {
		b.WriteString("\n\n")
		exit := PulseText("Press SPACE or q to exit and start building!", tick, "#a6e3a1")
		b.WriteString(centerTextWidth(exit, m.effectiveWidth()))
	}

	return b.String()
}

// renderNavigationBar renders the bottom navigation
func (m Model) renderNavigationBar() string {
	var b strings.Builder

	// Divider
	divider := styles.GradientDivider(m.width, "#45475a", "#313244")
	b.WriteString(divider + "\n")

	// Progress dots
	dots := ProgressDots(int(m.currentSlide), SlideCount, m.animTick)
	b.WriteString("  " + dots)

	// Slide counter
	counter := fmt.Sprintf(" %d/%d ", m.currentSlide+1, SlideCount)
	counterStyled := lipgloss.NewStyle().
		Background(lipgloss.Color("#313244")).
		Foreground(lipgloss.Color("#a6adc8")).
		Render(counter)

	// Navigation hints
	hints := []string{"← → navigate", "s skip", "q quit"}
	if m.tier >= layout.TierSplit {
		hints = append(hints, "space next")
	}
	if m.tier >= layout.TierWide {
		hints = append(hints, "1-9 jump")
	}
	if m.tier >= layout.TierUltra {
		hints = append(hints, "home/end first/last", "r restart", "tab expand")
	}
	navHints := "  " + strings.Join(hints, "  •  ")
	navStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Render(navHints)

	// Calculate spacing
	dotsLen := lipgloss.Width(dots)
	counterLen := lipgloss.Width(counter)
	navLen := lipgloss.Width(navHints)
	spacing := m.width - dotsLen - counterLen - navLen - 6

	if spacing > 0 {
		b.WriteString(strings.Repeat(" ", spacing))
	}

	b.WriteString(counterStyled + navStyled)

	return b.String()
}

// Helper function
func centerTextWidth(text string, width int) string {
	visLen := visibleLength(text)
	if visLen >= width {
		return text
	}
	padding := (width - visLen) / 2
	return strings.Repeat(" ", padding) + text
}
