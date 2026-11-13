package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/cli/tiers"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newLevelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "level",
		Short: "View and change CLI proficiency tier",
		Long: `View your current proficiency tier and change it.

NTM uses a tiered command system to avoid overwhelming new users:
  - Apprentice (Tier 1): Essential workflow commands only
  - Journeyman (Tier 2): Full standard commands
  - Master (Tier 3): Advanced features including robot mode

Examples:
  ntm level              # Show current tier and stats
  ntm level check        # Check session-aware promotion readiness
  ntm level up           # Promote to next tier
  ntm level down         # Demote to previous tier
  ntm level master       # Jump to Master tier
  ntm level apprentice   # Reset to Apprentice tier`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLevelShow()
		},
	}

	cmd.AddCommand(
		newLevelCheckCmd(),
		newLevelUpCmd(),
		newLevelDownCmd(),
		newLevelSetCmd("apprentice", tiers.TierApprentice),
		newLevelSetCmd("journeyman", tiers.TierJourneyman),
		newLevelSetCmd("master", tiers.TierMaster),
		newLevelResetCmd(),
	)

	return cmd
}

func newLevelCheckCmd() *cobra.Command {
	var sessionID string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check whether promotion should be suggested in this session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLevelCheck(sessionID)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "Session identifier used to deduplicate suggestions")
	return cmd
}

func runLevelCheck(sessionID string) error {
	cfg, err := config.LoadProficiency()
	if err != nil {
		return fmt.Errorf("failed to load proficiency config: %w", err)
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = strings.TrimSpace(os.Getenv("TMUX_PANE"))
	}
	if sessionID == "" {
		sessionID = fmt.Sprintf("ppid-%d", os.Getppid())
	}

	suggest, msg := cfg.CheckPromotion(sessionID)
	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Tier             int                      `json:"tier"`
			TierName         string                   `json:"tier_name"`
			Suggest          bool                     `json:"suggest"`
			Message          string                   `json:"message,omitempty"`
			UniqueCommands   int                      `json:"unique_commands"`
			PromotionHistory []config.PromotionRecord `json:"promotion_history"`
		}{
			Tier:             int(cfg.GetTier()),
			TierName:         cfg.GetTier().String(),
			Suggest:          suggest,
			Message:          msg,
			UniqueCommands:   cfg.GetUniqueCommandCount(),
			PromotionHistory: cfg.GetPromotionHistory(),
		})
	}
	if suggest && msg != "" {
		fmt.Println(msg)
	}
	return nil
}

func runLevelShow() error {
	cfg, err := config.LoadProficiency()
	if err != nil {
		return fmt.Errorf("failed to load proficiency config: %w", err)
	}

	t := theme.Current()
	currentTier := cfg.GetTier()
	stats := cfg.GetUsageStats()
	uniqueCommands := cfg.GetUniqueCommandCount()
	history := cfg.GetPromotionHistory()
	days := cfg.DaysSinceFirstUse()

	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Tier             int                      `json:"tier"`
			TierName         string                   `json:"tier_name"`
			Stats            config.UsageStats        `json:"stats"`
			UniqueCommands   int                      `json:"unique_commands"`
			DaysSinceFirst   int                      `json:"days_since_first_use"`
			PromotionHistory []config.PromotionRecord `json:"promotion_history"`
		}{
			Tier:             int(currentTier),
			TierName:         currentTier.String(),
			Stats:            stats,
			UniqueCommands:   uniqueCommands,
			DaysSinceFirst:   days,
			PromotionHistory: history,
		})
	}

	// Header
	headerStyle := lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	tierStyle := lipgloss.NewStyle().
		Foreground(getTierColor(currentTier, t)).
		Bold(true)

	fmt.Println()
	fmt.Printf("  %s %s (%s)\n\n",
		headerStyle.Render("Current proficiency:"),
		tierStyle.Render(currentTier.String()),
		lipgloss.NewStyle().Foreground(t.Subtext).Render(fmt.Sprintf("Tier %d", currentTier)),
	)

	// Description
	descStyle := lipgloss.NewStyle().Foreground(t.Text).Italic(true)
	fmt.Printf("  %s\n\n", descStyle.Render(currentTier.Description()))

	// Usage stats
	statsHeaderStyle := lipgloss.NewStyle().
		Foreground(t.Blue).
		Bold(true)

	labelStyle := lipgloss.NewStyle().
		Foreground(t.Subtext).
		Width(20)

	valueStyle := lipgloss.NewStyle().
		Foreground(t.Text)

	fmt.Printf("  %s\n", statsHeaderStyle.Render("Usage stats:"))
	fmt.Printf("    %s %s\n", labelStyle.Render("Commands run:"), valueStyle.Render(fmt.Sprintf("%d", stats.CommandsRun)))
	fmt.Printf("    %s %s\n", labelStyle.Render("Unique commands:"), valueStyle.Render(fmt.Sprintf("%d", uniqueCommands)))
	fmt.Printf("    %s %s\n", labelStyle.Render("Sessions created:"), valueStyle.Render(fmt.Sprintf("%d", stats.SessionsCreated)))
	fmt.Printf("    %s %s\n", labelStyle.Render("Prompts sent:"), valueStyle.Render(fmt.Sprintf("%d", stats.PromptsSent)))
	fmt.Printf("    %s %s\n\n", labelStyle.Render("Using NTM for:"), valueStyle.Render(fmt.Sprintf("%d days", days)))

	if len(history) > 0 {
		fmt.Printf("  %s\n", statsHeaderStyle.Render("Promotion history:"))
		start := 0
		if len(history) > 3 {
			start = len(history) - 3
		}
		for _, rec := range history[start:] {
			change := fmt.Sprintf("%s -> %s", tiers.Tier(rec.From).String(), tiers.Tier(rec.To).String())
			if rec.Reason != "" {
				change += fmt.Sprintf(" (%s)", rec.Reason)
			}
			fmt.Printf("    %s\n", valueStyle.Render(change))
		}
		fmt.Println()
	}

	// Next tier info
	if currentTier < tiers.TierMaster {
		nextTier := currentTier + 1
		nextStyle := lipgloss.NewStyle().
			Foreground(getTierColor(nextTier, t)).
			Bold(true)

		fmt.Printf("  %s %s\n", statsHeaderStyle.Render("Next tier:"), nextStyle.Render(nextTier.String()))
		fmt.Printf("    %s\n", descStyle.Render(getUnlocksDescription(nextTier)))
		fmt.Println()

		// Promotion suggestion
		if suggest, _, msg := cfg.ShouldSuggestPromotion(); suggest {
			suggestStyle := lipgloss.NewStyle().
				Foreground(t.Green).
				Italic(true)
			fmt.Printf("  %s\n\n", suggestStyle.Render(msg))
		}

		// Command hint
		hintStyle := lipgloss.NewStyle().Foreground(t.Overlay)
		cmdStyle := lipgloss.NewStyle().Foreground(t.Yellow).Bold(true)
		fmt.Printf("  %s %s %s\n\n",
			hintStyle.Render("Run"),
			cmdStyle.Render(fmt.Sprintf("ntm level %s", strings.ToLower(nextTier.String()))),
			hintStyle.Render("to unlock more features."),
		)
	} else {
		fullAccessStyle := lipgloss.NewStyle().
			Foreground(t.Green).
			Bold(true)
		fmt.Printf("  %s\n\n", fullAccessStyle.Render("You have full access to all NTM features!"))
	}

	return nil
}

func newLevelUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Promote to next tier",
		RunE: func(cmd *cobra.Command, args []string) error {
			return changeTier(1, "manual promotion")
		},
	}
}

func newLevelDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Demote to previous tier",
		RunE: func(cmd *cobra.Command, args []string) error {
			return changeTier(-1, "manual demotion")
		},
	}
}

func newLevelSetCmd(name string, tier tiers.Tier) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: fmt.Sprintf("Set tier to %s", tier.String()),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setTier(tier, "manual")
		},
	}
}

func newLevelResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Reset to Apprentice tier and clear usage stats",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadProficiency()
			if err != nil {
				return err
			}

			t := theme.Current()

			// Confirm
			fmt.Println()
			warnStyle := lipgloss.NewStyle().Foreground(t.Yellow).Bold(true)
			fmt.Printf("  %s This will reset your tier to Apprentice and clear all usage stats.\n\n",
				warnStyle.Render("Warning:"))

			fmt.Print("  Continue? [y/N]: ")
			var response string
			fmt.Scanln(&response)

			if response != "y" && response != "Y" {
				fmt.Println("  Cancelled.")
				return nil
			}

			if err := cfg.Reset(); err != nil {
				return err
			}

			successStyle := lipgloss.NewStyle().Foreground(t.Green).Bold(true)
			fmt.Printf("\n  %s Reset to Apprentice tier.\n\n", successStyle.Render("Done!"))
			return nil
		},
	}
}

func changeTier(delta int, reason string) error {
	cfg, err := config.LoadProficiency()
	if err != nil {
		return err
	}

	t := theme.Current()
	currentTier := cfg.GetTier()
	newTier := tiers.Tier(int(currentTier) + delta)

	// Bounds check
	if newTier < tiers.TierApprentice || newTier > tiers.TierMaster {
		warnStyle := lipgloss.NewStyle().Foreground(t.Yellow)
		if delta > 0 {
			fmt.Printf("\n  %s\n\n", warnStyle.Render("Already at maximum tier (Master)."))
		} else {
			fmt.Printf("\n  %s\n\n", warnStyle.Render("Already at minimum tier (Apprentice)."))
		}
		return nil
	}

	return setTier(newTier, reason)
}

func setTier(newTier tiers.Tier, reason string) error {
	cfg, err := config.LoadProficiency()
	if err != nil {
		return err
	}

	t := theme.Current()
	currentTier := cfg.GetTier()

	if currentTier == newTier {
		infoStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		fmt.Printf("\n  %s\n\n", infoStyle.Render(fmt.Sprintf("Already at %s tier.", newTier.String())))
		return nil
	}

	// Show what will change
	fmt.Println()

	if newTier > currentTier {
		// Upgrading - show new commands
		showTierChange(currentTier, newTier, t, true)
	} else {
		// Downgrading - show hidden commands
		showTierChange(newTier, currentTier, t, false)
	}

	// Apply change
	if err := cfg.SetTier(newTier, reason); err != nil {
		return err
	}

	successStyle := lipgloss.NewStyle().Foreground(t.Green).Bold(true)
	tierStyle := lipgloss.NewStyle().
		Foreground(getTierColor(newTier, t)).
		Bold(true)

	fmt.Printf("  %s Tier changed to %s.\n\n", successStyle.Render("Done!"), tierStyle.Render(newTier.String()))
	return nil
}

func showTierChange(fromTier, toTier tiers.Tier, t theme.Theme, upgrading bool) {
	headerStyle := lipgloss.NewStyle().Foreground(t.Blue).Bold(true)
	cmdStyle := lipgloss.NewStyle().Foreground(t.Primary)

	if upgrading {
		fmt.Printf("  %s\n", headerStyle.Render("New commands unlocked:"))
	} else {
		fmt.Printf("  %s\n", headerStyle.Render("Commands that will be hidden:"))
	}

	// Find commands that are between the tiers
	var commands []string
	for name, info := range tiers.Registry {
		if upgrading {
			// Show commands unlocked at toTier that weren't available at fromTier
			if info.Tier > fromTier && info.Tier <= toTier {
				commands = append(commands, name)
			}
		} else {
			// Show commands that will be hidden when going from toTier to fromTier
			if info.Tier > fromTier && info.Tier <= toTier {
				commands = append(commands, name)
			}
		}
	}

	if len(commands) == 0 {
		fmt.Printf("    (none)\n")
	} else {
		// Show up to 10 commands, then summarize
		for i, cmd := range commands {
			if i >= 10 {
				remaining := len(commands) - 10
				fmt.Printf("    ... and %d more\n", remaining)
				break
			}
			fmt.Printf("    %s\n", cmdStyle.Render(cmd))
		}
	}
	fmt.Println()
}

func getTierColor(tier tiers.Tier, t theme.Theme) lipgloss.Color {
	switch tier {
	case tiers.TierApprentice:
		return t.Green
	case tiers.TierJourneyman:
		return t.Blue
	case tiers.TierMaster:
		return t.Mauve
	default:
		return t.Text
	}
}

func getUnlocksDescription(tier tiers.Tier) string {
	switch tier {
	case tiers.TierJourneyman:
		return "Unlocks: dashboard, view, zoom, copy, save, palette, and more"
	case tiers.TierMaster:
		return "Unlocks: robot mode, file coordination, git worktrees, and advanced debugging"
	default:
		return ""
	}
}

func init() {
	// Ensure level command appears in help - will be added in root.go
	_ = os.Stdout // Silence unused import
}
