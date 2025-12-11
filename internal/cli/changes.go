package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newChangesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "changes [session]",
		Short: "Show recent file changes attributed to agents",
		Long: `Show which files were modified by agents in recent operations.

		This command tracks file modifications detected after 'ntm send' operations.
		If multiple agents were targeted, changes are attributed to all of them (potential conflict).

		Examples:
		  ntm changes              # All recent changes
		  ntm changes myproject    # Changes in specific session
		  ntm changes --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := ""
			if len(args) > 0 {
				session = args[0]
			}
			return runChanges(session)
		},
	}
	return cmd
}

func newConflictsCmd() *cobra.Command {
	var since string
	var limit int

	cmd := &cobra.Command{
		Use:   "conflicts [session]",
		Short: "Show potential file conflicts between agents",
		Long: `Identify files modified by multiple agents simultaneously.

		If you broadcast a prompt to multiple agents and they modify the same file,
		it's flagged as a conflict.

		Examples:
		  ntm conflicts
		  ntm conflicts myproject
		  ntm conflicts --since 6h --limit 10`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := ""
			if len(args) > 0 {
				session = args[0]
			}
			return runConflicts(session, since, limit)
		},
	}
	cmd.Flags().StringVar(&since, "since", "24h", "Look back window (e.g. 6h, 30m)")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum conflicts to display (0 = no limit)")
	return cmd
}

func runChanges(sessionFilter string) error {
	changes := tracker.RecordedChanges()

	// Filter and sort
	var filtered []tracker.RecordedFileChange
	for _, c := range changes {
		if sessionFilter == "" || c.Session == sessionFilter {
			filtered = append(filtered, c)
		}
	}

	// Sort by timestamp desc
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Timestamp.After(filtered[j].Timestamp)
	})

	if IsJSONOutput() {
		return output.PrintJSON(filtered)
	}

	if len(filtered) == 0 {
		fmt.Println("No file changes recorded.")
		return nil
	}

	t := theme.Current()
	fmt.Printf("%sRecent File Changes%s\n", "\033[1m", "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("─", 60), "\033[0m")

	for _, c := range filtered {
		age := formatAge(c.Timestamp)
		agents := strings.Join(c.Agents, ", ")

		changeType := ""
		switch c.Change.Type {
		case tracker.FileAdded:
			changeType = fmt.Sprintf("%sA%s", colorize(t.Success), "\033[0m")
		case tracker.FileDeleted:
			changeType = fmt.Sprintf("%sD%s", colorize(t.Error), "\033[0m")
		case tracker.FileModified:
			changeType = fmt.Sprintf("%sM%s", colorize(t.Warning), "\033[0m")
		}

		conflictMarker := ""
		if len(c.Agents) > 1 {
			conflictMarker = fmt.Sprintf(" %s(conflict?)%s", colorize(t.Error), "\033[0m")
		}

		// Show relative path if possible
		cwd, _ := os.Getwd()
		path := c.Change.Path
		if rel, err := os.Readlink(path); err == nil {
			path = rel
		} else if strings.HasPrefix(path, cwd) {
			path = path[len(cwd)+1:]
		}

		fmt.Printf("  %s %-30s  %s%s  %s%s%s\n",
			changeType,
			truncateStr(path, 30),
			colorize(t.Subtext), agents, "\033[0m",
			conflictMarker,
			fmt.Sprintf(" (%s)", age))
	}

	return nil
}

func runConflicts(sessionFilter, since string, limit int) error {
	window := 24 * time.Hour
	if since != "" {
		if d, err := time.ParseDuration(since); err == nil && d > 0 {
			window = d
		}
	}

	conflicts := tracker.ConflictsSince(time.Now().Add(-window), sessionFilter)
	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].LastAt.After(conflicts[j].LastAt)
	})
	if limit > 0 && len(conflicts) > limit {
		conflicts = conflicts[:limit]
	}

	if IsJSONOutput() {
		return output.PrintJSON(conflicts)
	}

	if len(conflicts) == 0 {
		fmt.Println("No conflicts detected.")
		return nil
	}

	t := theme.Current()
	fmt.Printf("%sConflicts Detected%s\n", "\033[1m", "\033[0m")
	fmt.Println("The following files were modified by different agent sets:")
	fmt.Println()

	for _, c := range conflicts {
		sevColor := t.Warning
		if c.Severity == "critical" {
			sevColor = t.Error
		}
		fmt.Printf("  %s[%s]%s %s%s%s\n",
			colorize(sevColor), strings.ToUpper(c.Severity), "\033[0m",
			colorize(t.Error), c.Path, "\033[0m")

		for _, change := range c.Changes {
			age := formatAge(change.Timestamp)
			agents := strings.Join(change.Agents, ", ")
			fmt.Printf("    %-24s %s (%s)\n", agents, change.Session, age)
		}
		fmt.Println()
	}

	return nil
}
