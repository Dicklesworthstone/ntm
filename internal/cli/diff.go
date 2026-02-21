package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newDiffCmd() *cobra.Command {
	var unified bool
	var sideBySide bool
	var codeOnly bool

	cmd := &cobra.Command{
		Use:   "diff [session] <pane1> <pane2>",
		Short: "Compare output from two agent panes",
		Long: `Compare outputs from different agents to see differences in approach.

You can specify panes by Index (e.g. 1) or Title (e.g. cc_1).
If session is omitted, it will be inferred from the current tmux session or project directory.

Examples:
  ntm diff myproject cc_1 cod_1
  ntm diff 1 2
  ntm diff 1 2 --unified
  ntm diff 1 2 --code-only`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session, pane1, pane2 string
			if len(args) == 3 {
				session = args[0]
				pane1 = args[1]
				pane2 = args[2]
			} else {
				pane1 = args[0]
				pane2 = args[1]
			}
			return runDiff(session, pane1, pane2, unified, sideBySide, codeOnly)
		},
	}

	cmd.Flags().BoolVarP(&unified, "unified", "u", false, "Show unified diff")
	cmd.Flags().BoolVar(&sideBySide, "side-by-side", false, "Show side-by-side diff (not implemented yet)")
	cmd.Flags().BoolVar(&codeOnly, "code-only", false, "Compare only extracted code blocks")

	return cmd
}

func runDiff(session, pane1ID, pane2ID string, unified, sideBySide, codeOnly bool) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	opts := SessionResolveOptions{}
	if IsJSONOutput() {
		opts.TreatAsJSON = true
	}
	res, err := ResolveSessionWithOptions(session, nil, opts)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return fmt.Errorf("session is required")
	}
	session = res.Session

	// Resolve panes
	p1, err := resolvePane(session, pane1ID)
	if err != nil {
		return err
	}
	p2, err := resolvePane(session, pane2ID)
	if err != nil {
		return err
	}

	// Capture output (default 1000 lines)
	out1, err := tmux.CapturePaneOutput(p1.ID, 1000)
	if err != nil {
		return fmt.Errorf("capturing pane 1: %w", err)
	}
	out2, err := tmux.CapturePaneOutput(p2.ID, 1000)
	if err != nil {
		return fmt.Errorf("capturing pane 2: %w", err)
	}

	content1 := out1
	content2 := out2

	if codeOnly {
		// Parse code blocks and join them
		blocks1 := codeblock.ExtractFromText(out1)
		blocks2 := codeblock.ExtractFromText(out2)

		var b1, b2 strings.Builder
		for _, b := range blocks1 {
			b1.WriteString(fmt.Sprintf("// %s\n%s\n\n", b.Language, b.Content))
		}
		for _, b := range blocks2 {
			b2.WriteString(fmt.Sprintf("// %s\n%s\n\n", b.Language, b.Content))
		}
		content1 = b1.String()
		content2 = b2.String()
	}

	res := output.ComputeDiff(p1.Title, content1, p2.Title, content2)

	if IsJSONOutput() {
		return output.PrintJSON(res)
	}

	fmt.Printf("Comparing %s vs %s:\n", p1.Title, p2.Title)
	fmt.Printf("  Lines: %d vs %d\n", res.LineCount1, res.LineCount2)
	fmt.Printf("  Similarity: %.1f%%\n", res.Similarity*100)

	if unified {
		fmt.Println("\nDiff:")
		fmt.Println(res.UnifiedDiff)
	} else if sideBySide {
		fmt.Println("\nSide-by-side diff is not implemented yet. Using summary.")
	}

	return nil
}

func resolvePane(session, idOrName string) (*tmux.Pane, error) {
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, err
	}

	// Try by Index
	for _, p := range panes {
		if fmt.Sprintf("%d", p.Index) == idOrName {
			return &p, nil
		}
	}

	// Try by Title (exact match)
	for _, p := range panes {
		if p.Title == idOrName {
			return &p, nil
		}
	}

	// Try by Title (suffix match, e.g. "cc_1" matches "myproject__cc_1")
	for _, p := range panes {
		if strings.HasSuffix(p.Title, idOrName) {
			return &p, nil
		}
	}

	// Try by ID
	for _, p := range panes {
		if p.ID == idOrName {
			return &p, nil
		}
	}

	return nil, fmt.Errorf("pane '%s' not found in session '%s'", idOrName, session)
}
