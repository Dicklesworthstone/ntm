package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// PrintStunningHelp prints a beautifully styled help output
func PrintStunningHelp() {
	t := theme.Current()
	ic := icons.Current()

	// Get terminal width
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
	}
	if width > 100 {
		width = 100
	}

	var b strings.Builder

	// ═══════════════════════════════════════════════════════════════
	// BANNER with gradient
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("\n")
	banner := components.RenderBanner(false, 0)
	for _, line := range strings.Split(banner, "\n") {
		b.WriteString("  " + line + "\n")
	}

	// Subtitle
	subtitle := components.RenderSubtitle("Named Tmux Session Manager for AI Agents")
	b.WriteString("  " + styles.CenterText(subtitle, 30) + "\n\n")

	// Gradient divider
	b.WriteString("  " + styles.GradientDivider(width-4,
		string(t.Blue), string(t.Lavender), string(t.Mauve)) + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// COMMAND SECTIONS
	// ═══════════════════════════════════════════════════════════════

	sections := []struct {
		title    string
		icon     string
		color    lipgloss.Color
		commands []commandHelp
	}{
		{
			title: "SESSION CREATION",
			icon:  ic.Rocket,
			color: t.Green,
			commands: []commandHelp{
				{"spawn", "sat", "<name> --cc=N --cod=N --gmi=N", "Create session and launch agents"},
				{"create", "cnt", "<name> [panes]", "Create empty session with N panes"},
				{"quick", "qps", "<name> [--template=go]", "Quick project setup with git, vscode"},
			},
		},
		{
			title: "AGENT MANAGEMENT",
			icon:  ic.Robot,
			color: t.Mauve,
			commands: []commandHelp{
				{"add", "ant", "<session> --cc=N --cod=N", "Add agents to existing session"},
				{"send", "bp", "<session> <prompt> [--agents]", "Send prompt to agents"},
				{"interrupt", "int", "<session>", "Send Ctrl+C to all agents"},
			},
		},
		{
			title: "SESSION NAVIGATION",
			icon:  ic.Window,
			color: t.Blue,
			commands: []commandHelp{
				{"attach", "rnt", "<session>", "Attach/switch to session"},
				{"list", "lnt", "", "List all tmux sessions"},
				{"status", "snt", "<session>", "Show detailed session status"},
				{"view", "vnt", "<session>", "Tile all panes and attach"},
				{"zoom", "znt", "<session> <pane|cc|cod|gmi>", "Zoom to specific pane"},
				{"dashboard", "", "<session>", "Interactive session dashboard"},
			},
		},
		{
			title: "OUTPUT MANAGEMENT",
			icon:  ic.Copy,
			color: t.Teal,
			commands: []commandHelp{
				{"copy", "cpnt", "<session> [--cc|--cod|--all]", "Copy pane output to clipboard"},
				{"save", "svnt", "<session> [output-dir]", "Save all outputs to files"},
			},
		},
		{
			title: "COMMAND PALETTE",
			icon:  ic.Palette,
			color: t.Pink,
			commands: []commandHelp{
				{"palette", "ncp", "[session]", "Open interactive command palette"},
				{"bind", "", "[key]", "Set up tmux F6 popup binding"},
			},
		},
		{
			title: "UTILITIES",
			icon:  ic.Gear,
			color: t.Yellow,
			commands: []commandHelp{
				{"deps", "cad", "", "Check agent CLI dependencies"},
				{"kill", "knt", "[-f] <session>", "Kill a session"},
				{"init", "", "<zsh|bash|fish>", "Generate shell integration"},
				{"config", "", "[show|edit|path]", "Manage configuration"},
			},
		},
	}

	for _, section := range sections {
		b.WriteString(renderSection(section.title, section.icon, section.color, section.commands, width, t, ic))
		b.WriteString("\n")
	}

	// ═══════════════════════════════════════════════════════════════
	// EXAMPLES
	// ═══════════════════════════════════════════════════════════════
	exampleSection := renderExamples(width, t, ic)
	b.WriteString(exampleSection)

	// ═══════════════════════════════════════════════════════════════
	// FOOTER
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("  " + styles.GradientDivider(width-4,
		string(t.Surface2), string(t.Surface1)) + "\n\n")

	// Quick reference
	aliasStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	aliasLine := aliasStyle.Render("  Aliases: cnt sat ant rnt lnt snt vnt znt bp int knt cpnt svnt ncp qps cad")
	b.WriteString(aliasLine + "\n")

	// Shell init hint
	hintStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
	cmdStyle := lipgloss.NewStyle().Foreground(t.Blue).Bold(true)
	b.WriteString("  " + hintStyle.Render("Add to shell: ") + cmdStyle.Render("eval \"$(ntm init zsh)\"") + "\n\n")

	fmt.Print(b.String())
}

type commandHelp struct {
	name  string
	alias string
	args  string
	desc  string
}

func renderSection(title, icon string, color lipgloss.Color, commands []commandHelp, width int, t theme.Theme, ic icons.IconSet) string {
	var b strings.Builder

	// Section header with icon and gradient
	headerText := icon + "  " + title
	headerGradient := styles.GradientText(headerText, string(color), string(t.Text))
	b.WriteString("  " + headerGradient + "\n")

	// Subtle underline
	underline := lipgloss.NewStyle().Foreground(t.Surface2).Render(strings.Repeat("─", len(title)+4))
	b.WriteString("  " + underline + "\n\n")

	// Commands table
	for _, cmd := range commands {
		line := renderCommandLine(cmd, t, ic)
		b.WriteString(line + "\n")
	}

	return b.String()
}

func renderCommandLine(cmd commandHelp, t theme.Theme, ic icons.IconSet) string {
	// Command name with color
	cmdStyle := lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true).
		Width(12)

	// Alias in parentheses
	aliasStyle := lipgloss.NewStyle().
		Foreground(t.Overlay).
		Width(6)

	// Arguments
	argStyle := lipgloss.NewStyle().
		Foreground(t.Green).
		Width(30)

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(t.Subtext)

	alias := ""
	if cmd.alias != "" {
		alias = "(" + cmd.alias + ")"
	}

	return fmt.Sprintf("    %s %s %s %s",
		cmdStyle.Render(cmd.name),
		aliasStyle.Render(alias),
		argStyle.Render(cmd.args),
		descStyle.Render(cmd.desc))
}

func renderExamples(width int, t theme.Theme, ic icons.IconSet) string {
	var b strings.Builder

	// Header
	headerText := ic.Lightning + "  QUICK START"
	headerGradient := styles.GradientText(headerText, string(t.Peach), string(t.Yellow))
	b.WriteString("  " + headerGradient + "\n")
	underline := lipgloss.NewStyle().Foreground(t.Surface2).Render(strings.Repeat("─", 15))
	b.WriteString("  " + underline + "\n\n")

	examples := []struct {
		cmd  string
		desc string
	}{
		{"ntm spawn myproject --cc=2 --cod=2", "Create session with 2 Claude + 2 Codex agents"},
		{"ntm send myproject \"fix the tests\"", "Send prompt to all agents"},
		{"ntm palette myproject", "Open command palette (or press F6)"},
		{"ntm view myproject", "Tile all panes and attach"},
		{"ntm dashboard myproject", "Open interactive dashboard"},
	}

	for _, ex := range examples {
		cmdStyle := lipgloss.NewStyle().
			Foreground(t.Yellow).
			Bold(true)

		descStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true)

		b.WriteString("    " + cmdStyle.Render(ex.cmd) + "\n")
		b.WriteString("    " + descStyle.Render("→ "+ex.desc) + "\n\n")
	}

	return b.String()
}

// PrintCompactHelp prints a more compact version for --help flag
func PrintCompactHelp() {
	t := theme.Current()
	ic := icons.Current()

	// Simple gradient title
	title := styles.GradientText("NTM - Named Tmux Manager",
		string(t.Blue), string(t.Mauve))
	fmt.Printf("\n  %s\n\n", title)

	// Brief command list
	fmt.Println("  " + lipgloss.NewStyle().Bold(true).Foreground(t.Text).Render("Commands:"))

	commands := []struct {
		name string
		desc string
	}{
		{"spawn", "Create session with AI agents"},
		{"send", "Send prompts to agents"},
		{"palette", "Interactive command palette"},
		{"status", "Show session status"},
		{"list", "List all sessions"},
		{"attach", "Attach to session"},
		{"view", "Tile and view all panes"},
		{"dashboard", "Interactive dashboard"},
	}

	cmdStyle := lipgloss.NewStyle().Foreground(t.Primary).Width(12)
	descStyle := lipgloss.NewStyle().Foreground(t.Subtext)

	for _, c := range commands {
		fmt.Printf("    %s %s\n", cmdStyle.Render(c.name), descStyle.Render(c.desc))
	}

	fmt.Println()
	hintStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
	fmt.Printf("  %s\n\n", hintStyle.Render("Run 'ntm' without arguments for full help, or 'ntm <command> --help' for details."))

	// Shell init hint
	cmdHighlight := lipgloss.NewStyle().Foreground(t.Blue).Bold(true)
	fmt.Printf("  Shell setup: %s\n\n", cmdHighlight.Render("eval \"$(ntm init zsh)\""))

	_ = ic // Use icons in future enhancements
}
