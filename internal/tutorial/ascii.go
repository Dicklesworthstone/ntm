package tutorial

import (
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
)

// Large NTM logo with extra flair
var LogoLarge = []string{
	"",
	"    ███╗   ██╗████████╗███╗   ███╗",
	"    ████╗  ██║╚══██╔══╝████╗ ████║",
	"    ██╔██╗ ██║   ██║   ██╔████╔██║",
	"    ██║╚██╗██║   ██║   ██║╚██╔╝██║",
	"    ██║ ╚████║   ██║   ██║ ╚═╝ ██║",
	"    ╚═╝  ╚═══╝   ╚═╝   ╚═╝     ╚═╝",
	"",
}

// Extra large logo for welcome screen
var LogoExtraLarge = []string{
	"",
	"    ██████   ██ ████████ ██████   ██████",
	"    ███░░██  ██ ░░░██░░░ ███░░██ ███░░██",
	"    ██ ░░██ ██     ██    ██  ░░████  ░██",
	"    ██  ░░███      ██    ██   ░░██   ██",
	"    ██   ░░██      ██    ██    ░░   ██",
	"    ██    ░██      ██    ██        ██",
	"    ░░     ░░      ░░    ░░        ░░",
	"",
}

// Tagline
var Tagline = "Named Tmux Manager — Multi-Agent Command Center"

// Subtitle
var Subtitle = "Orchestrate AI coding agents in parallel"

// Chaos diagram - multiple messy terminal windows
var ChaosDiagram = []string{
	"  +---------+ +---------+ +---------+",
	"  | Claude  | | Claude  | | Codex   |",
	"  | >_      | | >_      | | >_      |",
	"  +---------+ +---------+ +---------+",
	"        +---------+ +---------+",
	"        | Gemini  | | Claude  |",
	"        | >_      | | >_      |",
	"        +---------+ +---------+",
	"  +---------+         +---------+",
	"  | Codex   |         | Gemini  |",
	"  | >_      |         | >_      |",
	"  +---------+         +---------+",
}

// Order diagram - organized tmux session
var OrderDiagram = []string{
	"  +-------------------------------------------------------+",
	"  |              Session: myproject                       |",
	"  +-----------------+-----------------+-------------------+",
	"  |   You (shell)   |   Claude #1     |   Claude #2       |",
	"  |   $ ntm send    |   [Working...]  |   [Ready]         |",
	"  +-----------------+-----------------+-------------------+",
	"  |   Codex #1      |   Codex #2      |   Antigravity #1  |",
	"  |   [Testing...]  |   [Complete]    |   [Analyzing]     |",
	"  +-----------------+-----------------+-------------------+",
}

// Session concept diagram
var SessionDiagram = []string{
	"  +=========================================================+",
	"  |              TMUX SESSION: myproject                    |",
	"  +=========================================================+",
	"  |                                                         |",
	"  |   +--------------+  +--------------+  +--------------+  |",
	"  |   |  Pane 0      |  |  Pane 1      |  |  Pane 2      |  |",
	"  |   |  (You)       |  |  (Claude)    |  |  (Codex)     |  |",
	"  |   |   $ _        |  |   CC >_      |  |   COD >_     |  |",
	"  |   +--------------+  +--------------+  +--------------+  |",
	"  |                                                         |",
	"  +=========================================================+",
}

// Agent types visualization
var AgentsDiagram = []string{
	"",
	"     +----------------------------------------------------+",
	"     |                    AI AGENTS                       |",
	"     +----------------------------------------------------+",
	"",
	"       [CC] Claude       [COD] Codex      [AGY] Antigravity",
	"       -----------       -----------      -----------------",
	"        Anthropic          OpenAI           Google",
	"        Architecture       Implementation   Testing",
	"        Design             Code Gen         Analysis",
	"",
}

// Pane layout with agent assignments
var PaneLayoutDiagram = []string{
	"   +-----------------------------------------------------+",
	"   |  Pane Naming:  {session}__{agent}_{number}          |",
	"   +-----------------------------------------------------+",
	"   |                                                     |",
	"   |  myproject__cc_1    ->  Claude agent #1             |",
	"   |  myproject__cc_2    ->  Claude agent #2             |",
	"   |  myproject__cod_1   ->  Codex agent #1              |",
	"   |  myproject__agy_1   ->  Antigravity agent #1        |",
	"   |                                                     |",
	"   +-----------------------------------------------------+",
}

// Command flow diagram
var CommandFlowDiagram = []string{
	"",
	"                    +-------------+",
	"                    |  ntm send   |",
	"                    |   --all     |",
	"                    +------+------+",
	"                           |",
	"             +-------------+-------------+",
	"             |             |             |",
	"             v             v             v",
	"        +--------+   +--------+   +--------+",
	"        | Claude |   | Codex  |   |  agy   |",
	"        |  (cc)  |   | (cod)  |   | (agy)  |",
	"        +--------+   +--------+   +--------+",
	"",
}

// Workflow diagram
var WorkflowDiagram = []string{
	"",
	"    +============+     +============+     +============+",
	"    |  DESIGN    | --> |  IMPLEMENT | --> |   TEST     |",
	"    |  (Claude)  |     |  (Codex)   |     |  (Gemini)  |",
	"    +============+     +============+     +============+",
	"          |                  |                  |",
	"          +------------------+------------------+",
	"                             |",
	"                             v",
	"                      +------------+",
	"                      |  REVIEW    |",
	"                      |  (All)     |",
	"                      +------------+",
	"",
}

// Keyboard shortcuts reference
var KeyboardDiagram = []string{
	"",
	"   +------------------------------------------------------+",
	"   |               KEYBOARD SHORTCUTS                     |",
	"   +------------------------------------------------------+",
	"",
	"    Navigation                   Actions",
	"    ----------                   -------",
	"    <- -> / h l  Move slides     s   Skip animation",
	"    1-9          Jump to slide   r   Restart slide",
	"    Home/End     First/Last      q   Quit",
	"    Space        Next slide      ?   Help",
	"",
}

// Celebration banner
var CelebrationBanner = []string{
	"",
	"    * * * * * * * * * * * * * * * * * * *",
	"",
	"            YOU'RE READY!",
	"",
	"    * * * * * * * * * * * * * * * * * * *",
	"",
}

// Quick start commands
var QuickStartCommands = []string{
	"# Create a new project with agents",
	"$ ntm quick myproject --template=go",
	"$ ntm spawn myproject --cc=3 --cod=2 --agy=1",
	"",
	"# Send prompts to your agents",
	"$ ntm send myproject --all \"Build a REST API\"",
	"",
	"# Check status and manage",
	"$ ntm status myproject",
	"$ ntm view myproject",
}

// Tips content
var TipsContent = [][]string{
	{
		"[Tip #1] Start Small",
		"",
		"Begin with 1-2 agents per type.",
		"Scale up as needed with `ntm add`.",
	},
	{
		"[Tip #2] Divide & Conquer",
		"",
		"Use Claude for architecture,",
		"Codex for implementation,",
		"Antigravity for testing & docs.",
	},
	{
		"[Tip #3] Use the Palette",
		"",
		"Press F6 to open the command",
		"palette with pre-built prompts.",
	},
	{
		"[Tip #4] Save Your Work",
		"",
		"`ntm save myproject -o ~/logs`",
		"captures all agent outputs.",
	},
}

// Render functions for animated diagrams

// RenderAnimatedLogo renders the logo with animation
func RenderAnimatedLogo(tick int, width int) string {
	colors := []string{"#89b4fa", "#b4befe", "#cba6f7", "#f5c2e7"}

	var lines []string
	for i, line := range LogoLarge {
		// Staggered reveal
		if tick > i*3 {
			animLine := styles.Shimmer(line, tick-i*3, colors...)
			lines = append(lines, centerText(animLine, width))
		}
	}

	// Add tagline after logo is revealed
	if tick > len(LogoLarge)*3+10 {
		tagline := styles.GradientText(Tagline, "#a6adc8", "#6c7086")
		lines = append(lines, "")
		lines = append(lines, centerText(tagline, width))
	}

	// Add subtitle
	if tick > len(LogoLarge)*3+20 {
		subtitle := styles.GradientText(Subtitle, "#6c7086", "#45475a")
		lines = append(lines, centerText(subtitle, width))
	}

	return strings.Join(lines, "\n")
}

// RenderAnimatedChaosDiagram renders the chaos diagram with shake effect
func RenderAnimatedChaosDiagram(tick int, width int) string {
	var lines []string

	for _, line := range ChaosDiagram {
		padding := (width - len(line)) / 2
		if padding < 0 {
			padding = 0
		}

		// Red tinted for "bad"
		colored := styles.GradientText(line, "#f38ba8", "#fab387")
		lines = append(lines, strings.Repeat(" ", padding)+colored)
	}

	return strings.Join(lines, "\n")
}

// RenderAnimatedOrderDiagram renders the order diagram with glow effect
func RenderAnimatedOrderDiagram(tick int, width int) string {
	colors := []string{"#a6e3a1", "#94e2d5", "#89dceb", "#89b4fa"}

	var lines []string
	for i, line := range OrderDiagram {
		// Reveal line by line
		revealTick := tick - i*2
		if revealTick < 0 {
			continue
		}

		animLine := styles.Shimmer(line, revealTick, colors...)
		lines = append(lines, centerText(animLine, width))
	}

	return strings.Join(lines, "\n")
}

// RenderSessionDiagram renders the session concept diagram
func RenderSessionDiagram(tick int, step int, width int) string {
	colors := []string{"#89b4fa", "#b4befe", "#cba6f7"}
	if len(colors) == 0 {
		return ""
	}

	var lines []string

	// Reveal based on step
	maxLines := len(SessionDiagram)
	switch step {
	case 0:
		maxLines = 3 // Just header
	case 1:
		maxLines = 7 // Add panes structure
	}

	for i := 0; i < maxLines && i < len(SessionDiagram); i++ {
		line := SessionDiagram[i]
		animLine := styles.Shimmer(line, tick+i*2, colors...)
		lines = append(lines, centerText(animLine, width))
	}

	return strings.Join(lines, "\n")
}

// RenderAgentsDiagram renders the agents diagram with color coding
func RenderAgentsDiagram(tick int, width int) string {
	claudeColor := "#cba6f7"
	codexColor := "#89b4fa"
	geminiColor := "#f9e2af"

	var lines []string
	for i, line := range AgentsDiagram {
		var colored string

		// Apply agent-specific colors
		if strings.Contains(line, "Claude") || strings.Contains(line, "Anthropic") || strings.Contains(line, "Architecture") || strings.Contains(line, "Design") {
			colored = styles.GradientText(line, claudeColor, "#cdd6f4")
		} else if strings.Contains(line, "Codex") || strings.Contains(line, "OpenAI") || strings.Contains(line, "Implementation") || strings.Contains(line, "Code Gen") {
			colored = styles.GradientText(line, codexColor, "#cdd6f4")
		} else if strings.Contains(line, "Antigravity") || strings.Contains(line, "Gemini") || strings.Contains(line, "Google") || strings.Contains(line, "Testing") || strings.Contains(line, "Analysis") {
			colored = styles.GradientText(line, geminiColor, "#cdd6f4")
		} else if strings.Contains(line, "AI AGENTS") {
			colored = styles.Shimmer(line, tick, "#89b4fa", "#cba6f7", "#f5c2e7")
		} else {
			colored = styles.GradientText(line, "#6c7086", "#45475a")
		}

		// Staggered reveal
		if tick > i*3 {
			lines = append(lines, centerText(colored, width))
		}
	}

	return strings.Join(lines, "\n")
}

// RenderCommandFlowDiagram renders the command flow with animated arrows
func RenderCommandFlowDiagram(tick int, step int, width int) string {
	var lines []string

	// Arrow animation chars (ASCII only to match diagram)
	arrowChars := []string{"|", "!", "|", ":"}
	arrowChar := arrowChars[(tick/4)%len(arrowChars)]

	for i, line := range CommandFlowDiagram {
		reveal := tick - i*2
		if reveal < 0 {
			continue
		}

		// Animate arrow lines (lines with only | and spaces, no letters/+/-)
		// Arrow lines: "           |" and "    |     |     |"
		// Box lines have text like "Claude", "+", "-", etc.
		isArrowLine := strings.Contains(line, "|") && !containsLetters(line) && !strings.Contains(line, "+") && !strings.Contains(line, "-")
		if isArrowLine {
			line = strings.ReplaceAll(line, "|", arrowChar)
			if (tick/6)%2 == 0 {
				line = styles.GradientText(line, "#89b4fa", "#a6e3a1")
			} else {
				line = styles.GradientText(line, "#a6e3a1", "#89b4fa")
			}
		} else if strings.Contains(line, "ntm send") {
			line = styles.Shimmer(line, tick, "#89b4fa", "#cba6f7")
		} else if strings.Contains(line, "Claude") {
			line = styles.GradientText(line, "#cba6f7", "#b4befe")
		} else if strings.Contains(line, "Codex") {
			line = styles.GradientText(line, "#89b4fa", "#74c7ec")
		} else if strings.Contains(line, "Antigravity") || strings.Contains(line, "agy") || strings.Contains(line, "Gemini") {
			line = styles.GradientText(line, "#f9e2af", "#fab387")
		}

		lines = append(lines, centerText(line, width))
	}

	return strings.Join(lines, "\n")
}

// RenderWorkflowDiagram renders the workflow with step highlighting
func RenderWorkflowDiagram(tick int, activeStep int, width int) string {
	colors := map[int]string{
		0: "#cba6f7", // Design (Claude)
		1: "#89b4fa", // Implement (Codex)
		2: "#f9e2af", // Test (Gemini)
		3: "#a6e3a1", // Review (All)
	}

	var lines []string
	currentStep := (tick / 30) % 4

	for _, line := range WorkflowDiagram {
		colored := line

		// Highlight based on current step
		if strings.Contains(line, "DESIGN") || strings.Contains(line, "Claude") {
			if currentStep == 0 || activeStep == 0 {
				colored = styles.Shimmer(line, tick, colors[0], "#f5c2e7")
			} else {
				colored = styles.GradientText(line, colors[0], "#45475a")
			}
		} else if strings.Contains(line, "IMPLEMENT") || strings.Contains(line, "Codex") {
			if currentStep == 1 || activeStep == 1 {
				colored = styles.Shimmer(line, tick, colors[1], "#89dceb")
			} else {
				colored = styles.GradientText(line, colors[1], "#45475a")
			}
		} else if strings.Contains(line, "TEST") || strings.Contains(line, "Gemini") {
			if currentStep == 2 || activeStep == 2 {
				colored = styles.Shimmer(line, tick, colors[2], "#fab387")
			} else {
				colored = styles.GradientText(line, colors[2], "#45475a")
			}
		} else if strings.Contains(line, "REVIEW") || strings.Contains(line, "All") {
			if currentStep == 3 || activeStep == 3 {
				colored = styles.Shimmer(line, tick, colors[3], "#94e2d5")
			} else {
				colored = styles.GradientText(line, colors[3], "#45475a")
			}
		} else if strings.Contains(line, "▶") || strings.Contains(line, "──") {
			colored = styles.GradientText(line, "#6c7086", "#45475a")
		}

		lines = append(lines, centerText(colored, width))
	}

	return strings.Join(lines, "\n")
}

// RenderCelebration renders the celebration with particles
func RenderCelebration(tick int, width int) string {
	colors := []string{"#f38ba8", "#fab387", "#f9e2af", "#a6e3a1", "#89dceb", "#cba6f7", "#f5c2e7"}

	var lines []string
	for i, line := range CelebrationBanner {
		colorIdx := (tick/3 + i) % len(colors)
		nextColorIdx := (colorIdx + 1) % len(colors)

		colored := styles.Shimmer(line, tick+i*5, colors[colorIdx], colors[nextColorIdx])
		lines = append(lines, centerText(colored, width))
	}

	return strings.Join(lines, "\n")
}

// RenderCommandCode renders command code with syntax highlighting
func RenderCommandCode(commands []string, tick int, typewriter bool) string {
	var lines []string

	commentColor := "#6c7086"
	commandColor := "#a6e3a1"
	argColor := "#89b4fa"
	flagColor := "#f9e2af"
	stringColor := "#f5c2e7"

	totalChars := 0
	if typewriter {
		for _, cmd := range commands {
			totalChars += len(cmd)
		}
	}

	visibleChars := tick * 2
	if !typewriter {
		visibleChars = totalChars + 1000
	}

	charCount := 0
	for _, cmd := range commands {
		if charCount > visibleChars {
			break
		}

		var line strings.Builder
		remaining := visibleChars - charCount

		// Determine line type and color
		if strings.HasPrefix(cmd, "#") {
			// Comment
			visible := cmd
			if len(visible) > remaining {
				visible = visible[:remaining]
			}
			line.WriteString(applyColor(visible, commentColor))
		} else if strings.HasPrefix(cmd, "$") {
			// Command
			parts := strings.Fields(cmd)
			for i, part := range parts {
				if charCount > visibleChars {
					break
				}

				partLen := len(part)
				if charCount+partLen > visibleChars {
					partLen = visibleChars - charCount
					part = part[:partLen]
				}

				if i == 0 {
					line.WriteString(applyColor(part, "#6c7086")) // $
				} else if i == 1 {
					line.WriteString(" " + applyColor(part, commandColor)) // ntm
				} else if strings.HasPrefix(part, "--") {
					line.WriteString(" " + applyColor(part, flagColor))
				} else if strings.HasPrefix(part, "-") {
					line.WriteString(" " + applyColor(part, flagColor))
				} else if strings.HasPrefix(part, "\"") {
					line.WriteString(" " + applyColor(part, stringColor))
				} else {
					line.WriteString(" " + applyColor(part, argColor))
				}

				charCount += len(part) + 1
			}
		} else {
			// Regular line
			visible := cmd
			if len(visible) > remaining {
				visible = visible[:remaining]
			}
			line.WriteString(applyColor(visible, "#cdd6f4"))
		}

		charCount += len(cmd)
		lines = append(lines, line.String())
	}

	// Add cursor if typewriter mode
	if typewriter && charCount <= visibleChars && (tick/8)%2 == 0 {
		if len(lines) > 0 {
			lines[len(lines)-1] += applyColor("▌", "#89b4fa")
		}
	}

	return strings.Join(lines, "\n")
}

// Helper functions

func centerText(text string, width int) string {
	visLen := visibleLength(text)
	if visLen >= width {
		return text
	}
	padding := (width - visLen) / 2
	return strings.Repeat(" ", padding) + text
}

func applyColor(text, hex string) string {
	color := styles.ParseHex(hex)
	return "\x1b[38;2;" + itoa(color.R) + ";" + itoa(color.G) + ";" + itoa(color.B) + "m" + text + "\x1b[0m"
}

func itoa(i int) string {
	if i < 0 {
		return "-" + itoa(-i)
	}
	if i < 10 {
		return string(rune('0' + i))
	}
	if i < 100 {
		return string(rune('0'+i/10)) + string(rune('0'+i%10))
	}
	return string(rune('0'+i/100)) + string(rune('0'+(i/10)%10)) + string(rune('0'+i%10))
}

// containsLetters checks if a string contains any ASCII letters
func containsLetters(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}
