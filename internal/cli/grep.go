package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// GrepMatch aliases the canonical ORI grep match shape in internal/robot.
type GrepMatch = robot.GrepMatch

// GrepResult contains all matches from a grep operation
type GrepResult struct {
	Pattern         string      `json:"pattern"`
	Session         string      `json:"session"`
	Matches         []GrepMatch `json:"matches"`
	TotalLines      int         `json:"total_lines_searched"`
	MatchCount      int         `json:"match_count"`
	PaneCount       int         `json:"panes_searched"`
	CaseInsensitive bool        `json:"case_insensitive,omitempty"`
}

// Text outputs the grep result as human-readable text
func (r *GrepResult) Text(w io.Writer) error {
	t := theme.Current()

	if len(r.Matches) == 0 {
		fmt.Fprintf(w, "%sNo matches found%s for pattern '%s'\n",
			colorize(t.Warning), colorize(t.Text), r.Pattern)
		return nil
	}

	// Group matches by pane for cleaner output
	lastKey := ""
	for i, m := range r.Matches {
		// Print pane header when pane changes
		key := m.Session + "/" + m.Pane
		if key != lastKey {
			if i > 0 {
				fmt.Fprintln(w, "--")
			}
			lastKey = key
		}

		// Print context before (if any)
		if len(m.Context) > 0 {
			contextBefore := m.ContextBefore
			if contextBefore < 0 || contextBefore > len(m.Context) ||
				(contextBefore == 0 && m.ContextAfter == 0) {
				contextBefore = len(m.Context) / 2
			}
			for j := 0; j < contextBefore && j < len(m.Context); j++ {
				lineNum := m.Line - contextBefore + j
				if lineNum > 0 {
					fmt.Fprintf(w, "%s%s/%s:%d-%s %s\n",
						colorize(t.Surface1), m.Session, m.Pane, lineNum, colorize(t.Text), m.Context[j])
				}
			}
		}

		// Print matching line with highlighting
		fmt.Fprintf(w, "%s%s/%s:%d:%s %s\n",
			colorize(t.Blue), m.Session, m.Pane, m.Line, colorize(t.Text),
			highlightMatch(m.Content, r.Pattern, r.CaseInsensitive, t))

		// Print context after (if any)
		if len(m.Context) > 0 {
			contextBefore := m.ContextBefore
			if contextBefore < 0 || contextBefore > len(m.Context) ||
				(contextBefore == 0 && m.ContextAfter == 0) {
				contextBefore = len(m.Context) / 2
			}
			for j := contextBefore; j < len(m.Context); j++ {
				lineNum := m.Line + j - contextBefore + 1
				fmt.Fprintf(w, "%s%s/%s:%d-%s %s\n",
					colorize(t.Surface1), m.Session, m.Pane, lineNum, colorize(t.Text), m.Context[j])
			}
		}
	}

	fmt.Fprintf(w, "\n%s%d matches%s in %d pane(s)\n",
		colorize(t.Success), r.MatchCount, colorize(t.Text), r.PaneCount)

	return nil
}

// JSON returns the JSON-serializable data
func (r *GrepResult) JSON() interface{} {
	return r
}

func newGrepCmd() *cobra.Command {
	var (
		caseInsensitive bool
		invertMatch     bool
		contextLines    int
		afterLines      int
		beforeLines     int
		maxLines        int
		listOnly        bool
		allFlag         bool
		filter          AgentFilter
	)

	cmd := &cobra.Command{
		Use:   "grep <pattern> [session-name]",
		Short: "Search pane output with regex",
		Long: `Search across all pane output buffers with regex support.

Searches the captured output from tmux panes for lines matching
the given pattern. Supports standard regex syntax.

Examples:
  ntm grep 'error' myproject          # Search all panes
  ntm grep 'error' myproject --cc     # Only Claude panes
  ntm grep 'def.*auth' myproject -i   # Case insensitive
  ntm grep 'TODO' myproject -C 3      # 3 lines context
  ntm grep 'pattern' myproject -n 100 # Search last 100 lines
  ntm grep 'pattern' --all            # Search all sessions`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := args[0]
			var session string
			if len(args) > 1 {
				session = args[1]
			}

			context := contextLines
			if afterLines > 0 || beforeLines > 0 {
				context = 0
			}

			opts := GrepOptions{
				AllSessions:     allFlag,
				CaseInsensitive: caseInsensitive,
				InvertMatch:     invertMatch,
				ContextLines:    context,
				AfterLines:      afterLines,
				BeforeLines:     beforeLines,
				MaxLines:        maxLines,
				ListOnly:        listOnly,
				Filter:          filter,
			}

			return runGrep(pattern, session, opts)
		},
	}

	cmd.Flags().BoolVarP(&caseInsensitive, "ignore-case", "i", false, "Case insensitive search")
	cmd.Flags().BoolVarP(&invertMatch, "invert-match", "v", false, "Invert match (show non-matching lines)")
	cmd.Flags().IntVarP(&contextLines, "context", "C", 0, "Show N lines of context")
	cmd.Flags().IntVarP(&afterLines, "after-context", "A", 0, "Show N lines after match")
	cmd.Flags().IntVarP(&beforeLines, "before-context", "B", 0, "Show N lines before match")
	cmd.Flags().IntVarP(&maxLines, "max-lines", "n", 1000, "Search last N lines per pane")
	cmd.Flags().BoolVarP(&listOnly, "files-with-matches", "l", false, "List matching panes only")
	cmd.Flags().BoolVar(&allFlag, "all", false, "Search all sessions")
	bindAgentFilterFlags(cmd, &filter, "Search %s panes only")
	cmd.ValidArgsFunction = completeSessionSecondArg

	return cmd
}

// GrepOptions contains options for the grep operation
type GrepOptions struct {
	AllSessions     bool
	CaseInsensitive bool
	InvertMatch     bool
	ContextLines    int
	AfterLines      int
	BeforeLines     int
	MaxLines        int
	ListOnly        bool
	Filter          AgentFilter
}

func runGrep(pattern, session string, opts GrepOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}
	if _, err := robot.CompileGrepRegex(pattern, opts.CaseInsensitive); err != nil {
		return err
	}

	if !opts.AllSessions {
		res, err := ResolveSession(session, os.Stdout)
		if err != nil {
			return err
		}
		if res.Session == "" {
			return nil
		}
		res.ExplainIfInferred(os.Stderr)
		session = res.Session
		if !tmux.SessionExists(session) {
			return fmt.Errorf("session '%s' not found", session)
		}
	} else {
		sessionList, err := tmux.ListSessions()
		if err != nil {
			return err
		}
		if len(sessionList) == 0 {
			return fmt.Errorf("no tmux sessions found")
		}
	}

	out, err := robot.GetGrep(robot.GrepOptions{
		Pattern:         pattern,
		Session:         session,
		AllSessions:     opts.AllSessions,
		AgentTypes:      opts.Filter.SelectedTypes(),
		IncludeAllPanes: opts.Filter.All,
		CaseInsensitive: opts.CaseInsensitive,
		InvertMatch:     opts.InvertMatch,
		ContextLines:    opts.ContextLines,
		BeforeLines:     opts.BeforeLines,
		AfterLines:      opts.AfterLines,
		MaxLines:        opts.MaxLines,
		ListOnly:        opts.ListOnly,
	})
	if err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("%s", out.Error)
	}
	for _, warn := range out.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warn)
	}

	if opts.ListOnly {
		matchingPanes := make(map[string]bool, len(out.MatchingPanes))
		for _, p := range out.MatchingPanes {
			matchingPanes[p] = true
		}
		return outputMatchingPanes(matchingPanes)
	}

	result := &GrepResult{
		Pattern:         out.Pattern,
		Session:         out.Session,
		Matches:         out.Matches,
		TotalLines:      out.TotalLines,
		MatchCount:      out.MatchCount,
		PaneCount:       out.PaneCount,
		CaseInsensitive: out.CaseInsensitive,
	}
	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

func outputMatchingPanes(panes map[string]bool) error {
	t := theme.Current()

	if jsonOutput {
		paneList := make([]string, 0, len(panes))
		for p := range panes {
			paneList = append(paneList, p)
		}
		sort.Strings(paneList)
		return output.PrintJSON(map[string]interface{}{
			"matching_panes": paneList,
			"count":          len(paneList),
		})
	}

	if len(panes) == 0 {
		fmt.Printf("%sNo matching panes found%s\n", colorize(t.Warning), colorize(t.Text))
		return nil
	}

	paneList := make([]string, 0, len(panes))
	for p := range panes {
		paneList = append(paneList, p)
	}
	sort.Strings(paneList)
	for _, p := range paneList {
		fmt.Printf("%s%s%s\n", colorize(t.Blue), p, colorize(t.Text))
	}
	return nil
}

// highlightMatch highlights the matched portion in the line
func highlightMatch(line, pattern string, caseInsensitive bool, t theme.Theme) string {
	// Build pattern with optional case-insensitivity to match actual search behavior
	rePattern := "(" + pattern + ")"
	if caseInsensitive {
		rePattern = "(?i)" + rePattern
	}
	re, err := regexp.Compile(rePattern)
	if err != nil {
		// If pattern fails to compile as regex, use escaped literal
		rePattern = "(" + regexp.QuoteMeta(pattern) + ")"
		if caseInsensitive {
			rePattern = "(?i)" + rePattern
		}
		re, err = regexp.Compile(rePattern)
		if err != nil {
			return line // Can't highlight, return as-is
		}
	}

	highlighted := re.ReplaceAllStringFunc(line, func(match string) string {
		return fmt.Sprintf("%s%s%s", colorize(t.Yellow), match, colorize(t.Text))
	})

	return highlighted
}
