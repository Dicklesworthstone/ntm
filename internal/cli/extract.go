package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/clipboard"
	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/spf13/cobra"
)

// ExtractResponse is the JSON output format for extract command.
type ExtractResponse struct {
	output.TimestampedResponse
	Session    string                `json:"session"`
	Source     string                `json:"source"` // Session or session:pane
	Blocks     []codeblock.CodeBlock `json:"blocks"`
	TotalFound int                   `json:"total_found"`
}

func newExtractCmd() *cobra.Command {
	var (
		language   string
		lastPane   bool
		lines      int
		paneIndex  string
		copyFlag   bool
		applyFlag  bool
		selectFlag int
	)

	cmd := &cobra.Command{
		Use:   "extract <session> [pane]",
		Short: "Extract code blocks from agent output",
		Long: `Extract markdown code blocks from agent pane output.

Examples:
  ntm extract myproject              # Extract from all panes
  ntm extract myproject cc_1         # Extract from specific pane
  ntm extract myproject --last       # From most recent active pane
  ntm extract myproject --lang=python  # Only Python blocks
  ntm extract myproject --copy       # Copy all blocks to clipboard
  ntm extract myproject --copy -s 1  # Copy specific block (1-indexed)
  ntm extract myproject --apply      # Apply blocks to detected files
  ntm extract myproject --json       # Output as JSON`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := args[0]

			// Get optional pane argument
			if len(args) > 1 {
				paneIndex = args[1]
			}

			return runExtract(sessionName, paneIndex, language, lastPane, lines, copyFlag, applyFlag, selectFlag)
		},
	}

	cmd.Flags().StringVarP(&language, "lang", "l", "", "Filter by language (e.g., python, go, bash)")
	cmd.Flags().BoolVar(&lastPane, "last", false, "Extract from most recent active pane")
	cmd.Flags().IntVarP(&lines, "lines", "n", 500, "Number of lines to capture from pane")
	cmd.Flags().BoolVarP(&copyFlag, "copy", "c", false, "Copy extracted code to clipboard")
	cmd.Flags().BoolVarP(&applyFlag, "apply", "a", false, "Apply code blocks to detected files")
	cmd.Flags().IntVarP(&selectFlag, "select", "s", 0, "Select specific block by number (1-indexed)")

	return cmd
}

func runExtract(sessionName, paneIndex, language string, lastPane bool, lines int, copyFlag, applyFlag bool, selectBlock int) error {
	// Check session exists
	if !tmux.SessionExists(sessionName) {
		if IsJSONOutput() {
			return output.PrintJSON(output.NewErrorWithCode("SESSION_NOT_FOUND",
				fmt.Sprintf("session '%s' does not exist", sessionName)))
		}
		return fmt.Errorf("session '%s' does not exist", sessionName)
	}

	// Get panes
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		if IsJSONOutput() {
			return output.PrintJSON(output.NewErrorWithDetails("failed to get panes", err.Error()))
		}
		return fmt.Errorf("failed to get panes: %w", err)
	}

	// Filter panes
	var targetPanes []tmux.Pane
	if lastPane {
		// Find most recently active pane
		for _, p := range panes {
			if p.Active {
				targetPanes = []tmux.Pane{p}
				break
			}
		}
		if len(targetPanes) == 0 && len(panes) > 0 {
			targetPanes = []tmux.Pane{panes[len(panes)-1]}
		}
	} else if paneIndex != "" {
		// Find specific pane by index or title
		for _, p := range panes {
			paneIdxStr := fmt.Sprintf("%d", p.Index)
			if paneIdxStr == paneIndex || p.Title == paneIndex || strings.Contains(p.Title, paneIndex) {
				targetPanes = []tmux.Pane{p}
				break
			}
		}
		if len(targetPanes) == 0 {
			if IsJSONOutput() {
				return output.PrintJSON(output.NewErrorWithCode("PANE_NOT_FOUND",
					fmt.Sprintf("pane '%s' not found", paneIndex)))
			}
			return fmt.Errorf("pane '%s' not found in session '%s'", paneIndex, sessionName)
		}
	} else {
		// All panes
		targetPanes = panes
	}

	// Create parser with optional language filter
	parser := codeblock.NewParser()
	if language != "" {
		parser = parser.WithLanguageFilter([]string{language})
	}

	// Extract from all target panes
	var allBlocks []codeblock.CodeBlock
	source := sessionName
	if paneIndex != "" {
		source = fmt.Sprintf("%s:%s", sessionName, paneIndex)
	}

	for _, pane := range targetPanes {
		// Capture pane output
		captured, err := tmux.CapturePaneOutput(pane.ID, lines)
		if err != nil {
			continue // Skip panes that fail
		}

		// Parse code blocks
		blocks := parser.Parse(captured)

		// Add source pane info
		for i := range blocks {
			blocks[i].SourcePane = fmt.Sprintf("%s:%d", sessionName, pane.Index)
		}

		allBlocks = append(allBlocks, blocks...)
	}

	// Filter by selection if specified
	if selectBlock > 0 {
		if selectBlock > len(allBlocks) {
			if IsJSONOutput() {
				return output.PrintJSON(output.NewErrorWithCode("INVALID_SELECTION",
					fmt.Sprintf("block %d does not exist (found %d blocks)", selectBlock, len(allBlocks))))
			}
			return fmt.Errorf("block %d does not exist (found %d blocks)", selectBlock, len(allBlocks))
		}
		allBlocks = []codeblock.CodeBlock{allBlocks[selectBlock-1]}
	}

	// Output
	if IsJSONOutput() {
		response := ExtractResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             sessionName,
			Source:              source,
			Blocks:              allBlocks,
			TotalFound:          len(allBlocks),
		}
		return output.PrintJSON(response)
	}

	// Text output
	if len(allBlocks) == 0 {
		fmt.Printf("No code blocks found in %s\n", source)
		return nil
	}

	// Handle copy flag
	if copyFlag {
		return handleExtractCopy(allBlocks)
	}

	// Handle apply flag
	if applyFlag {
		return handleExtractApply(allBlocks)
	}

	// Default: list blocks
	fmt.Printf("Found %d code block(s) in %s:\n\n", len(allBlocks), source)

	for i, block := range allBlocks {
		// Header
		langDisplay := block.Language
		if langDisplay == "" {
			langDisplay = "text"
		}

		fileInfo := ""
		if block.FilePath != "" {
			if block.IsNew {
				fileInfo = fmt.Sprintf(" (new file: %s)", block.FilePath)
			} else {
				fileInfo = fmt.Sprintf(" (%s)", block.FilePath)
			}
		}

		fmt.Printf("[%d] %s%s (from %s)\n", i+1, langDisplay, fileInfo, block.SourcePane)

		// Box around content
		fmt.Println("    ┌" + strings.Repeat("─", 60))
		for _, line := range strings.Split(block.Content, "\n") {
			if len(line) > 58 {
				line = line[:55] + "..."
			}
			fmt.Printf("    │ %s\n", line)
		}
		fmt.Println("    └" + strings.Repeat("─", 60))
		fmt.Println()
	}

	return nil
}

// handleExtractCopy copies code blocks to clipboard
func handleExtractCopy(blocks []codeblock.CodeBlock) error {
	if len(blocks) == 0 {
		return fmt.Errorf("no code blocks to copy")
	}

	// Build combined content
	var parts []string
	for i, block := range blocks {
		if len(blocks) > 1 {
			// Add separator for multiple blocks
			langDisplay := block.Language
			if langDisplay == "" {
				langDisplay = "text"
			}
			parts = append(parts, fmt.Sprintf("// === Block %d (%s) ===", i+1, langDisplay))
		}
		parts = append(parts, block.Content)
	}
	combined := strings.Join(parts, "\n\n")

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

	lineCount := strings.Count(combined, "\n") + 1
	fmt.Printf("✓ Copied %d code block(s) (%d lines) to clipboard\n", len(blocks), lineCount)
	return nil
}

// handleExtractApply applies code blocks to files
func handleExtractApply(blocks []codeblock.CodeBlock) error {
	if len(blocks) == 0 {
		return fmt.Errorf("no code blocks to apply")
	}

	applied := 0
	skipped := 0

	for i, block := range blocks {
		if block.FilePath == "" {
			fmt.Printf("[%d] Skipped: no file path detected\n", i+1)
			skipped++
			continue
		}

		// Show what we're about to do
		langDisplay := block.Language
		if langDisplay == "" {
			langDisplay = "text"
		}

		action := "update"
		if block.IsNew {
			action = "create"
		}

		fmt.Printf("[%d] %s %s (%s)\n", i+1, action, block.FilePath, langDisplay)

		// Check if file exists
		_, err := os.Stat(block.FilePath)
		fileExists := err == nil

		if fileExists && !block.IsNew {
			// Show diff preview (first few lines)
			fmt.Printf("    Preview: %d lines of code\n", strings.Count(block.Content, "\n")+1)
		}

		// Ask for confirmation
		fmt.Printf("    Apply? [y]es / [n]o / [s]kip all: ")
		var response string
		fmt.Scanln(&response)

		switch strings.ToLower(strings.TrimSpace(response)) {
		case "y", "yes":
			// Write the file
			if err := os.WriteFile(block.FilePath, []byte(block.Content), 0644); err != nil {
				fmt.Printf("    ✗ Failed to write: %v\n", err)
				continue
			}
			fmt.Printf("    ✓ Applied to %s\n", block.FilePath)
			applied++
		case "s", "skip":
			fmt.Printf("Skipping remaining blocks\n")
			break
		default:
			fmt.Printf("    Skipped\n")
			skipped++
		}
	}

	fmt.Printf("\nApplied: %d, Skipped: %d\n", applied, skipped)
	return nil
}

func init() {
	// Note: This will be added to rootCmd in root.go's init()
}
