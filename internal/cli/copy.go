package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/clipboard"
	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/palette"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newCopyCmd() *cobra.Command {
	var (
		lines    int
		pattern  string
		allFlag  bool
		ccFlag   bool
		codFlag  bool
		gmiFlag  bool
		codeFlag bool
		headers  bool
		outFile  string
		quiet    bool
	)

	cmd := &cobra.Command{
		Use:     "copy [session[:pane]]",
		Aliases: []string{"cp", "yank"},
		Short:   "Copy pane output to clipboard",
		Long: `Copy the output from one or more panes to the system clipboard.

By default, captures the last 1000 lines from each pane.
Use filters to target specific agent types.

Examples:
  ntm copy myproject            # Copy from current/selected pane
  ntm copy myproject --all      # Copy from all panes
  ntm copy myproject --cc       # Copy from Claude panes only
  ntm copy myproject -l 500     # Copy last 500 lines
  ntm copy myproject --code     # Copy only code blocks
  ntm copy myproject --pattern "ERROR" # Copy lines matching regex
  ntm copy myproject:1 --last 50       # Copy specific pane by index
  ntm copy myproject --output out.txt  # Save to file instead of clipboard`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			var paneSelector string
			if len(args) > 0 {
				// Allow session:pane syntax for direct targeting
				parts := strings.SplitN(args[0], ":", 2)
				session = parts[0]
				if len(parts) == 2 {
					paneSelector = parts[1]
				}
			}

			filter := AgentFilter{
				All:    allFlag,
				Claude: ccFlag,
				Codex:  codFlag,
				Gemini: gmiFlag,
			}

			options := CopyOptions{
				Lines:    lines,
				Pattern:  pattern,
				Code:     codeFlag,
				Headers:  headers,
				Output:   outFile,
				Quiet:    quiet,
				PaneSpec: paneSelector,
			}

			clip, err := clipboard.New()
			if err != nil {
				return fmt.Errorf("failed to init clipboard: %w", err)
			}

			return runCopy(cmd.OutOrStdout(), clip, session, filter, options)
		},
	}

	cmd.Flags().IntVarP(&lines, "lines", "l", 1000, "Number of lines to capture")
	cmd.Flags().StringVarP(&pattern, "pattern", "p", "", "Regex pattern to filter lines")
	cmd.Flags().BoolVar(&codeFlag, "code", false, "Copy only code blocks")
	cmd.Flags().BoolVar(&headers, "headers", false, "Include pane headers (defaults off when --code is set)")
	cmd.Flags().BoolVar(&allFlag, "all", false, "Copy from all panes")
	cmd.Flags().BoolVar(&ccFlag, "cc", false, "Copy from Claude panes")
	cmd.Flags().BoolVar(&codFlag, "cod", false, "Copy from Codex panes")
	cmd.Flags().BoolVar(&gmiFlag, "gmi", false, "Copy from Gemini panes")
	cmd.Flags().StringVarP(&outFile, "output", "o", "", "Write output to file instead of clipboard")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress confirmation output")

	return cmd
}

// AgentFilter specifies which agent types to target
type AgentFilter struct {
	All    bool
	Claude bool
	Codex  bool
	Gemini bool
}

func (f AgentFilter) IsEmpty() bool {
	return !f.All && !f.Claude && !f.Codex && !f.Gemini
}

func (f AgentFilter) Matches(agentType tmux.AgentType) bool {
	if f.All {
		return true
	}
	switch agentType {
	case tmux.AgentClaude:
		return f.Claude
	case tmux.AgentCodex:
		return f.Codex
	case tmux.AgentGemini:
		return f.Gemini
	default:
		return false
	}
}

// CopyOptions defines options for the copy command
type CopyOptions struct {
	Lines    int
	Pattern  string
	Code     bool
	Headers  bool
	Output   string
	Quiet    bool
	PaneSpec string
}

type copyResult struct {
	Sources     []string `json:"sources"`
	Lines       int      `json:"lines"`
	Bytes       int      `json:"bytes"`
	Destination string   `json:"destination"`
	Pattern     string   `json:"pattern,omitempty"`
	Code        bool     `json:"code,omitempty"`
	OutputPath  string   `json:"output_path,omitempty"`
}

func runCopy(w io.Writer, clip clipboard.Clipboard, session string, filter AgentFilter, opts CopyOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	t := theme.Current()

	// Determine target session
	if session == "" {
		if tmux.InTmux() {
			session = tmux.GetCurrentSession()
		} else {
			if !IsInteractive(w) {
				return fmt.Errorf("non-interactive environment: session name is required")
			}
			sessions, err := tmux.ListSessions()
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				return fmt.Errorf("no tmux sessions found")
			}

			selected, err := palette.RunSessionSelector(sessions)
			if err != nil {
				return err
			}
			if selected == "" {
				return nil
			}
			session = selected
		}
	}

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return err
	}

	// Filter panes
	var targetPanes []tmux.Pane
	if opts.PaneSpec != "" {
		// Direct pane targeting overrides agent filters
		for _, p := range panes {
			if paneMatchesSelector(p, opts.PaneSpec) {
				targetPanes = []tmux.Pane{p}
				break
			}
		}
	} else if filter.IsEmpty() {
		// No filter: copy from active pane or first pane
		for _, p := range panes {
			if p.Active {
				targetPanes = []tmux.Pane{p}
				break
			}
		}
		if len(targetPanes) == 0 && len(panes) > 0 {
			targetPanes = []tmux.Pane{panes[0]}
		}
	} else {
		for _, p := range panes {
			if filter.Matches(p.Type) {
				targetPanes = append(targetPanes, p)
			}
		}
	}

	if len(targetPanes) == 0 {
		return fmt.Errorf("no matching panes found")
	}

	// Compile regex if provided
	var regex *regexp.Regexp
	if opts.Pattern != "" {
		var err error
		regex, err = regexp.Compile(opts.Pattern)
		if err != nil {
			return fmt.Errorf("invalid pattern regex: %w", err)
		}
	}

	// Capture output from all target panes
	var outputs []string
	var sourceLabels []string
	for _, p := range targetPanes {
		output, err := tmux.CapturePaneOutput(p.ID, opts.Lines)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to capture pane %d: %v\n", p.Index, err)
			continue
		}

		output = filterOutput(output, regex, opts.Code)

		if strings.TrimSpace(output) == "" {
			continue
		}

		includeHeaders := true
		if opts.Code {
			// Default to headerless for code mode; allow opt-in via flag.
			includeHeaders = opts.Headers
		}
		if includeHeaders {
			header := fmt.Sprintf("═══ %s (pane %d) ═══", p.Title, p.Index)
			outputs = append(outputs, header, output, "")
		} else {
			outputs = append(outputs, output)
		}
		sourceLabels = append(sourceLabels, fmt.Sprintf("%s:%d", session, p.Index))
	}

	if len(outputs) == 0 {
		return fmt.Errorf("no content captured (check filters)")
	}

	combined := strings.Join(outputs, "\n")

	lineCount := strings.Count(combined, "\n")
	bytesCount := len([]byte(combined))

	destination := "clipboard"
	if opts.Output != "" {
		if err := os.MkdirAll(filepath.Dir(opts.Output), 0o755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("failed to create output directory: %w", err)
		}
		if err := os.WriteFile(opts.Output, []byte(combined), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		destination = "file"
	} else {
		clip, err := clipboard.New()
		if err != nil {
			return fmt.Errorf("failed to init clipboard: %w", err)
		}
		if !clip.Available() {
			return fmt.Errorf("clipboard backend unavailable")
		}
		if err := clip.Copy(combined); err != nil {
			return fmt.Errorf("failed to copy to clipboard via %s: %w", clip.Backend(), err)
		}
	}

	result := copyResult{
		Sources:     sourceLabels,
		Lines:       lineCount,
		Bytes:       bytesCount,
		Destination: destination,
		Pattern:     opts.Pattern,
		Code:        opts.Code,
	}
	if opts.Output != "" {
		result.OutputPath = opts.Output
	}

	formatter := output.DefaultFormatter(jsonOutput)
	return formatter.OutputData(result, func(w io.Writer) error {
		if opts.Quiet {
			return nil
		}
		targetLabel := destination
		if destination == "file" {
			targetLabel = fmt.Sprintf("file (%s)", opts.Output)
		}
		fmt.Fprintf(w, "%s✓%s Copied %d lines from %d pane(s) to %s\n",
			colorize(t.Success), colorize(t.Text), lineCount, len(sourceLabels), targetLabel)
		return nil
	})
}

// paneMatchesSelector matches pane index or pane ID (with or without % prefix)
func paneMatchesSelector(p tmux.Pane, selector string) bool {
	if selector == "" {
		return false
	}
	// Match numeric pane index
	if idx, err := strconv.Atoi(selector); err == nil {
		return p.Index == idx
	}
	// Match pane id formats: %12 or 12
	if selector == p.ID || selector == strings.TrimPrefix(p.ID, "%") {
		return true
	}
	// Match tmux target suffix like "1.2" (window.pane) by suffix match
	if strings.HasSuffix(p.ID, selector) {
		return true
	}
	return false
}

// filterOutput applies regex line filtering then optional code-block extraction.
func filterOutput(output string, regex *regexp.Regexp, codeOnly bool) string {
	if regex != nil {
		lines := strings.Split(output, "\n")
		var filtered []string
		for _, line := range lines {
			if regex.MatchString(line) {
				filtered = append(filtered, line)
			}
		}
		output = strings.Join(filtered, "\n")
	}

	if !codeOnly {
		return output
	}

	blocks := codeblock.ExtractFromText(output)
	var blockContents []string
	for _, b := range blocks {
		if b.Content != "" {
			blockContents = append(blockContents, b.Content)
		}
	}
	if len(blockContents) == 0 {
		return ""
	}
	return strings.Join(blockContents, "\n\n")
}
