package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/integrations/caut"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const quotaStalenessThreshold = 5 * time.Minute

func newQuotaCmd() *cobra.Command {
	var (
		showRateLimits bool
		clearCooldown  string
		resetProvider  string
		resetAll       bool
		clearCache     bool
	)

	cmd := &cobra.Command{
		Use:   "quota [session]",
		Short: "Check agent quota usage and manage provider cooldowns",
		Long: `Query agents for their current quota usage and inspect or reset adaptive provider rate-limit cooldowns.
Sends /usage command to supported agents (Claude) and parses the output.

Examples:
  ntm quota myproject
  ntm quota --json
  ntm quota --rate-limits
  ntm quota --clear-cooldown anthropic
  ntm quota --reset-rate-limit openai
  ntm quota --reset-all-rate-limits
  ntm quota --clear-cache`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if clearCache {
				caut.GetGlobalPoller().GetCache().Clear()
				if len(args) == 0 && !showRateLimits && clearCooldown == "" && resetProvider == "" && !resetAll {
					if IsJSONOutput() {
						return output.PrintJSON(map[string]any{
							"success":       true,
							"cache_cleared": true,
						})
					}
					fmt.Fprintln(cmd.OutOrStdout(), "Cleared cached caut usage data.")
					return nil
				}
			}

			if showRateLimits || clearCooldown != "" || resetProvider != "" || resetAll {
				return runQuotaRateLimitOps(cmd.OutOrStdout(), showRateLimits, clearCooldown, resetProvider, resetAll)
			}

			var session string
			if len(args) > 0 {
				session = args[0]
			}

			res, err := ResolveSession(session, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if res.Session == "" {
				return nil
			}
			res.ExplainIfInferred(os.Stderr)

			session = res.Session
			return runQuota(session)
		},
	}

	cmd.Flags().BoolVar(&showRateLimits, "rate-limits", false, "Display adaptive provider rate-limit and cooldown state")
	cmd.Flags().StringVar(&clearCooldown, "clear-cooldown", "", "Clear active cooldown for a provider (or 'all')")
	cmd.Flags().StringVar(&resetProvider, "reset-rate-limit", "", "Reset learned rate-limit state and history for a provider")
	cmd.Flags().BoolVar(&resetAll, "reset-all-rate-limits", false, "Reset learned rate-limit state and history for all providers")
	cmd.Flags().BoolVar(&clearCache, "clear-cache", false, "Clear cached caut quota usage data")

	return cmd
}

type quotaRateLimitProviderView struct {
	Provider           string                     `json:"provider"`
	InCooldown         bool                       `json:"in_cooldown"`
	CooldownRemainingS int                        `json:"cooldown_remaining_sec,omitempty"`
	CurrentDelay       string                     `json:"current_delay"`
	TotalRateLimits    int                        `json:"total_rate_limits"`
	TotalSuccesses     int                        `json:"total_successes"`
	RecentEvents       []ratelimit.RateLimitEvent `json:"recent_events,omitempty"`
}

func runQuotaRateLimitOps(out io.Writer, showOnly bool, clearCooldown, resetProvider string, resetAll bool) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	tracker := ratelimit.NewRateLimitTracker(dir)
	if err := tracker.LoadFromDir(dir); err != nil {
		return err
	}

	mutated := false
	action := "inspect"
	if resetAll {
		tracker.ResetAll()
		mutated = true
		action = "reset_all"
	}
	if trimmed := strings.TrimSpace(resetProvider); trimmed != "" {
		tracker.Reset(trimmed)
		mutated = true
		action = "reset_provider"
	}
	if trimmed := strings.TrimSpace(clearCooldown); trimmed != "" {
		if strings.EqualFold(trimmed, "all") {
			for _, p := range tracker.GetAllProviders() {
				tracker.ClearCooldown(p)
			}
		} else {
			tracker.ClearCooldown(trimmed)
		}
		mutated = true
		action = "clear_cooldown"
	}
	if mutated {
		if err := tracker.SaveToDir(dir); err != nil {
			return err
		}
	}

	providers := tracker.GetAllProviders()
	sort.Strings(providers)
	views := make([]quotaRateLimitProviderView, 0, len(providers))
	for _, p := range providers {
		st := tracker.GetProviderState(p)
		if st == nil {
			continue
		}
		inCooldown := tracker.IsInCooldown(p)
		rem := tracker.CooldownRemaining(p)
		views = append(views, quotaRateLimitProviderView{
			Provider:           p,
			InCooldown:         inCooldown,
			CooldownRemainingS: int(rem.Round(time.Second).Seconds()),
			CurrentDelay:       ratelimit.FormatDelay(st.CurrentDelay),
			TotalRateLimits:    st.TotalRateLimits,
			TotalSuccesses:     st.TotalSuccesses,
			RecentEvents:       tracker.GetRecentEvents(p, 5),
		})
	}

	if IsJSONOutput() {
		return output.PrintJSON(map[string]any{
			"success":   true,
			"action":    action,
			"providers": views,
		})
	}

	if mutated && !showOnly {
		fmt.Fprintf(out, "Updated rate-limit state (%s).\n", action)
		if len(views) == 0 {
			return nil
		}
	}
	if len(views) == 0 {
		fmt.Fprintln(out, "No recorded provider rate-limit state.")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Provider\tCooldown\tDelay\tRateLimits\tSuccesses\tRecentEvents")
	fmt.Fprintln(w, "────────\t────────\t─────\t──────────\t─────────\t────────────")
	for _, v := range views {
		cd := "none"
		if v.InCooldown {
			cd = fmt.Sprintf("active (%ds)", v.CooldownRemainingS)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\n",
			v.Provider, cd, v.CurrentDelay, v.TotalRateLimits, v.TotalSuccesses, len(v.RecentEvents))
	}
	return w.Flush()
}

func runQuota(session string) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return err
	}

	fetcher := &quota.PTYFetcher{
		CommandTimeout: 5 * time.Second,
	}

	var results []*quota.QuotaInfo
	ctx := context.Background()

	if !IsJSONOutput() {
		fmt.Printf("Querying quota for session '%s'...\n", session)
	}

	for _, p := range panes {
		provider, ok := quotaProviderForAgentType(p.Type)
		if !ok {
			continue // Skip user/unknown panes
		}

		if !IsJSONOutput() {
			fmt.Printf("  Checking pane %d (%s)...\n", p.Index, provider)
		}

		info, err := fetcher.FetchQuota(ctx, p.ID, provider)
		if err != nil {
			if !IsJSONOutput() {
				fmt.Printf("    Error: %v\n", err)
			}
			continue
		}
		if info.Error != "" {
			if !IsJSONOutput() {
				fmt.Printf("    Error: %s\n", info.Error)
			}
		}
		if info.Unsupported != "" && !IsJSONOutput() {
			fmt.Printf("    %s: %s\n", provider, info.Unsupported)
		}
		info.PaneIndex = p.Index
		results = append(results, info)
	}

	if IsJSONOutput() {
		return output.PrintJSON(results)
	}

	printQuotaTable(results)
	return nil
}

func quotaProviderForAgentType(agentType tmux.AgentType) (quota.Provider, bool) {
	switch tmux.AgentType(agentType).Canonical() {
	case tmux.AgentClaude:
		return quota.ProviderClaude, true
	case tmux.AgentCodex:
		return quota.ProviderCodex, true
	case tmux.AgentGemini, tmux.AgentAntigravity:
		// Antigravity (agy) shares Google's quota/auth with the Gemini CLI.
		return quota.ProviderGemini, true
	case tmux.AgentOmp:
		// Reported explicitly as unsupported rather than silently skipped.
		return quota.ProviderOmp, true
	default:
		return "", false
	}
}

func normalizedProviderName(agentType tmux.AgentType) string {
	if provider, ok := quotaProviderForAgentType(agentType); ok {
		return string(provider)
	}
	if canonical := tmux.AgentType(agentType).Canonical(); canonical != "" && canonical != tmux.AgentUnknown {
		return string(canonical)
	}
	return string(agentType)
}

func printQuotaTable(results []*quota.QuotaInfo) {
	formatQuotaTable(os.Stdout, results)
}

func formatQuotaTable(out io.Writer, results []*quota.QuotaInfo) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Pane\tProvider\tHealth\tSession\tWeekly\tReset\tAccount")
	fmt.Fprintln(w, "────\t────────\t──────\t───────\t──────\t─────\t───────")

	healthyCount := 0
	staleCount := 0
	for _, r := range results {
		sess := "-"
		if r.SessionUsage > 0 {
			sess = fmt.Sprintf("%.1f%%", r.SessionUsage)
		}
		weekly := "-"
		if r.WeeklyUsage > 0 {
			weekly = fmt.Sprintf("%.1f%%", r.WeeklyUsage)
		}

		health := string(r.Health())
		if r.IsHealthy() {
			healthyCount++
		}
		if !r.FetchedAt.IsZero() && r.IsStale(quotaStalenessThreshold) {
			staleCount++
			health += " (stale)"
		}

		account := r.AccountID
		if account == "" {
			account = "-"
		}
		if r.Unsupported != "" {
			account = "no quota API (provider-error classification)"
		}

		reset := r.ResetString
		if reset == "" {
			reset = "-"
		}

		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.PaneIndex, r.Provider, health, sess, weekly, reset, account)
	}
	_ = w.Flush()
	if len(results) > 0 {
		fmt.Fprintf(out, "\nHealthy panes: %d/%d", healthyCount, len(results))
		if staleCount > 0 {
			fmt.Fprintf(out, " (%d stale)", staleCount)
		}
		fmt.Fprintln(out)
	}
}
