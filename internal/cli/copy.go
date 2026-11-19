package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/clipboard"
	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newCopyCmd() *cobra.Command {
	var (
		last      int
		pattern   string
		allFlag   bool
		filter    AgentFilter
		codeFlag  bool
		pasteFlag bool
		headers   bool
		outFile   string
		quiet     bool
	)

	cmd := &cobra.Command{
		Use:     "copy [session[:pane]]",
		Aliases: []string{"cp", "yank"},
		Short:   "Copy pane output to clipboard (or read clipboard with --paste)",
		Long: `Copy the output from one or more panes to the system clipboard, or read
from the system clipboard with --paste.

By default, captures the last 1000 lines from the selected pane.
Use filters to target specific agent types.

Examples:
  ntm copy myproject            # Copy from current/selected pane
  ntm copy myproject --all      # Copy from all panes
  ntm copy myproject --cc       # Copy from Claude panes only
  ntm copy myproject -l 500     # Copy last 500 lines
  ntm copy myproject --code     # Copy only code blocks
  ntm copy myproject --pattern "ERROR" # Copy lines matching regex
  ntm copy myproject:1 --last 50       # Copy specific pane by index
  ntm copy myproject --output out.txt  # Save to file instead of clipboard
  ntm copy --paste                     # Print current clipboard contents
  ntm copy --paste --code              # Extract code blocks from clipboard`,
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

			runFilter := filter
			runFilter.All = allFlag

			options := CopyOptions{
				Last:     last,
				Pattern:  pattern,
				Code:     codeFlag,
				Paste:    pasteFlag,
				Headers:  headers,
				Output:   outFile,
				Quiet:    quiet,
				PaneSpec: paneSelector,
			}

			if options.Paste {
				if len(args) > 0 {
					return fmt.Errorf("--paste reads from the clipboard and does not accept a session argument")
				}
				return runPaste(cmd.OutOrStdout(), options)
			}

			return runCopy(cmd.OutOrStdout(), session, runFilter, options)
		},
	}

	cmd.Flags().IntVarP(&last, "last", "l", 1000, "Number of most recent lines to capture")
	cmd.Flags().StringVarP(&pattern, "pattern", "p", "", "Regex pattern to filter lines")
	cmd.Flags().BoolVar(&codeFlag, "code", false, "Copy only code blocks")
	cmd.Flags().BoolVar(&pasteFlag, "paste", false, "Read from system clipboard instead of tmux panes")
	cmd.Flags().BoolVar(&headers, "headers", false, "Include pane headers (defaults off when --code is set)")
	cmd.Flags().BoolVar(&allFlag, "all", false, "Copy from all panes")
	bindAgentFilterFlags(cmd, &filter, "Copy from %s panes")
	cmd.Flags().StringVarP(&outFile, "output", "o", "", "Write output to file instead of clipboard")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress confirmation output")
	cmd.ValidArgsFunction = completeSessionColonPane

	return cmd
}

// AgentFilter specifies which agent types to target across all 11 built-in harnesses.
type AgentFilter struct {
	All         bool
	Claude      bool
	Codex       bool
	Gemini      bool
	Antigravity bool
	Grok        bool
	Cursor      bool
	Windsurf    bool
	Aider       bool
	Omp         bool
	Opencode    bool
	Ollama      bool
}

func bindAgentFilterFlags(cmd *cobra.Command, filter *AgentFilter, descFmt string) {
	cmd.Flags().BoolVar(&filter.Claude, "cc", false, fmt.Sprintf(descFmt, "Claude"))
	cmd.Flags().BoolVar(&filter.Codex, "cod", false, fmt.Sprintf(descFmt, "Codex"))
	cmd.Flags().BoolVar(&filter.Gemini, "gmi", false, fmt.Sprintf(descFmt, "Gemini"))
	cmd.Flags().BoolVar(&filter.Antigravity, "agy", false, fmt.Sprintf(descFmt, "Antigravity"))
	cmd.Flags().BoolVar(&filter.Grok, "grok", false, fmt.Sprintf(descFmt, "Grok Build"))
	cmd.Flags().BoolVar(&filter.Cursor, "cursor", false, fmt.Sprintf(descFmt, "Cursor"))
	cmd.Flags().BoolVar(&filter.Windsurf, "windsurf", false, fmt.Sprintf(descFmt, "Windsurf"))
	cmd.Flags().BoolVar(&filter.Aider, "aider", false, fmt.Sprintf(descFmt, "Aider"))
	cmd.Flags().BoolVar(&filter.Opencode, "oc", false, fmt.Sprintf(descFmt, "OpenCode (oc)"))
	cmd.Flags().BoolVar(&filter.Omp, "omp", false, fmt.Sprintf(descFmt, "Oh My Pi (omp)"))
	cmd.Flags().BoolVar(&filter.Ollama, "ollama", false, fmt.Sprintf(descFmt, "Ollama"))
}

func (f AgentFilter) IsEmpty() bool {
	return !f.All && !f.Claude && !f.Codex && !f.Gemini && !f.Antigravity &&
		!f.Grok && !f.Cursor && !f.Windsurf && !f.Aider && !f.Omp && !f.Opencode && !f.Ollama
}

func (f AgentFilter) SelectedTypes() []string {
	var types []string
	if f.Claude {
		types = append(types, "cc")
	}
	if f.Codex {
		types = append(types, "cod")
	}
	if f.Gemini {
		types = append(types, "gmi")
	}
	if f.Antigravity {
		types = append(types, "agy")
	}
	if f.Grok {
		types = append(types, "grok")
	}
	if f.Cursor {
		types = append(types, "cursor")
	}
	if f.Windsurf {
		types = append(types, "windsurf")
	}
	if f.Aider {
		types = append(types, "aider")
	}
	if f.Opencode {
		types = append(types, "oc")
	}
	if f.Omp {
		types = append(types, "omp")
	}
	if f.Ollama {
		types = append(types, "ollama")
	}
	return types
}

func (f AgentFilter) Matches(agentType tmux.AgentType) bool {
	if f.All {
		return true
	}
	switch tmux.AgentType(agentType).Canonical() {
	case tmux.AgentClaude:
		return f.Claude
	case tmux.AgentCodex:
		return f.Codex
	case tmux.AgentGemini:
		return f.Gemini
	case tmux.AgentAntigravity:
		return f.Antigravity
	case tmux.AgentGrok:
		return f.Grok
	case tmux.AgentCursor:
		return f.Cursor
	case tmux.AgentWindsurf:
		return f.Windsurf
	case tmux.AgentAider:
		return f.Aider
	case tmux.AgentOmp:
		return f.Omp
	case tmux.AgentOpencode:
		return f.Opencode
	case tmux.AgentOllama:
		return f.Ollama
	default:
		return false
	}
}

// CopyOptions defines options for the copy command
type CopyOptions struct {
	Last     int
	Pattern  string
	Code     bool
	Paste    bool
	Headers  bool
	Output   string
	Quiet    bool
	PaneSpec string
}

type copyResult struct {
	Source      string            `json:"source"`
	Panes       []string          `json:"panes"`
	Lines       int               `json:"lines"`
	Bytes       int               `json:"bytes"`
	Destination string            `json:"destination"`
	Pattern     string            `json:"pattern,omitempty"`
	Code        bool              `json:"code,omitempty"`
	OutputPath  string            `json:"output_path,omitempty"`
	Content     string            `json:"content,omitempty"`
	Redaction   *RedactionSummary `json:"redaction,omitempty"`
}

func runPaste(w io.Writer, opts CopyOptions) error {
	var regex *regexp.Regexp
	if opts.Pattern != "" {
		var err error
		regex, err = regexp.Compile(opts.Pattern)
		if err != nil {
			return fmt.Errorf("invalid pattern regex: %w", err)
		}
	}

	clip, err := clipboard.New()
	if err != nil {
		return fmt.Errorf("failed to init clipboard: %w", err)
	}
	if !clip.Available() {
		return fmt.Errorf("clipboard backend unavailable")
	}

	pasted, err := clip.Paste()
	if err != nil {
		return fmt.Errorf("failed to paste from clipboard via %s: %w", clip.Backend(), err)
	}

	filtered := filterOutput(pasted, regex, opts.Code)

	redactCfg := redaction.DefaultConfig()
	if cfg != nil {
		redactCfg = cfg.Redaction.ToRedactionLibConfig()
	}
	redacted, redactionSummary, err := applyOutputRedaction(filtered, redactCfg)
	if err != nil {
		return err
	}
	if redactionSummary != nil {
		cats := formatRedactionCategoryCounts(redactionSummary.Categories)
		switch redactionSummary.Action {
		case "warn":
			fmt.Fprintf(os.Stderr, "Warning: detected %d potential secret(s) (%s); output is NOT modified (mode=warn)\n", redactionSummary.Findings, cats)
		case "redact":
			fmt.Fprintf(os.Stderr, "Redacted %d potential secret(s) (%s)\n", redactionSummary.Findings, cats)
		}
	}

	trimmed := strings.TrimRight(redacted, "\n")
	lineCount := 0
	if trimmed != "" {
		lineCount = strings.Count(trimmed, "\n") + 1
	}

	destination := "stdout"
	if opts.Output != "" {
		if err := os.MkdirAll(filepath.Dir(opts.Output), 0o755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("failed to create output directory: %w", err)
		}
		if err := os.WriteFile(opts.Output, []byte(redacted), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		destination = "file"
	}

	result := copyResult{
		Source:      "clipboard",
		Panes:       []string{},
		Lines:       lineCount,
		Bytes:       len(redacted),
		Destination: destination,
		Pattern:     opts.Pattern,
		Code:        opts.Code,
		OutputPath:  opts.Output,
		Redaction:   redactionSummary,
	}
	if destination == "stdout" {
		result.Content = redacted
	}

	if IsJSONOutput() {
		return output.DefaultFormatter(true).OutputData(result, nil)
	}
	if destination == "file" {
		if !opts.Quiet {
			t := theme.Current()
			fmt.Fprintf(w, "%s✓%s Saved %d clipboard line(s) to file (%s)\n",
				colorize(t.Success), colorize(t.Text), lineCount, opts.Output)
		}
		return nil
	}
	_, err = io.WriteString(w, redacted)
	return err
}

func runCopy(w io.Writer, session string, filter AgentFilter, opts CopyOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	if opts.Last <= 0 {
		return fmt.Errorf("--last must be positive")
	}

	t := theme.Current()

	res, err := ResolveSessionWithOptions(session, w, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
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
	var paneLabels []string
	for _, p := range targetPanes {
		output, err := tmux.CapturePaneOutput(p.ID, opts.Last)
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
		paneLabels = append(paneLabels, fmt.Sprintf("%s:.%d", session, p.Index))
	}

	if len(outputs) == 0 {
		return fmt.Errorf("no content captured (check filters)")
	}

	combined := strings.Join(outputs, "\n")

	redactCfg := redaction.DefaultConfig()
	if cfg != nil {
		redactCfg = cfg.Redaction.ToRedactionLibConfig()
	}

	redacted, redactionSummary, err := applyOutputRedaction(combined, redactCfg)
	if err != nil {
		return err
	}
	if redactionSummary != nil {
		cats := formatRedactionCategoryCounts(redactionSummary.Categories)
		switch redactionSummary.Action {
		case "warn":
			fmt.Fprintf(os.Stderr, "Warning: detected %d potential secret(s) (%s); output is NOT modified (mode=warn)\n", redactionSummary.Findings, cats)
		case "redact":
			fmt.Fprintf(os.Stderr, "Redacted %d potential secret(s) (%s)\n", redactionSummary.Findings, cats)
		}
	}
	combined = redacted

	trimmed := strings.TrimRight(combined, "\n")
	lineCount := 0
	if trimmed != "" {
		lineCount = strings.Count(trimmed, "\n") + 1
	}
	bytesCount := len(combined)

	destination := "clipboard"
	if opts.Output != "" {
		// Make side effects (mkdir/write) happen only after redaction preflight.
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

	source := session
	if opts.PaneSpec != "" {
		source = fmt.Sprintf("%s:%s", session, opts.PaneSpec)
	}

	result := copyResult{
		Source:      source,
		Panes:       paneLabels,
		Lines:       lineCount,
		Bytes:       bytesCount,
		Destination: destination,
		Pattern:     opts.Pattern,
		Code:        opts.Code,
		Redaction:   redactionSummary,
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
			colorize(t.Success), colorize(t.Text), lineCount, len(paneLabels), targetLabel)
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

// filterOutput applies optional code-block extraction then regex line filtering.
func filterOutput(output string, regex *regexp.Regexp, codeOnly bool) string {
	if codeOnly {
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
		output = strings.Join(blockContents, "\n\n")
	}

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

	return output
}
