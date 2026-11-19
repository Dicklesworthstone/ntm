package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/clipboard"
	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
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
	// --copy/--apply are interactive side-effect modes; under --json the JSON
	// envelope is the whole contract and those side effects do not run. They
	// used to be silently dropped (the JSON path returned before the copy or
	// apply handling) — now the combination fails loudly instead
	// (bd-ws7-docs-ux-truth-tqh3l.4).
	if IsJSONOutput() && (copyFlag || applyFlag) {
		combo := "--copy"
		if applyFlag && copyFlag {
			combo = "--copy/--apply"
		} else if applyFlag {
			combo = "--apply"
		}
		cause := fmt.Errorf("%s is not supported with --json output", combo)
		return emitJSONFailureEnvelopeWithCause(
			output.NewErrorFull(
				"UNSUPPORTED_COMBINATION",
				cause.Error(),
				"",
				"Run without --json to use "+combo+", or parse blocks[] from the JSON output and act on it yourself",
			),
			cause,
		)
	}

	out, err := robot.GetExtract(robot.ExtractOptions{
		Session:     sessionName,
		Pane:        paneIndex,
		Language:    language,
		LastPane:    lastPane,
		Lines:       lines,
		SelectBlock: selectBlock,
	})
	if err != nil {
		return err
	}
	if !out.Success {
		cause := fmt.Errorf("%s", out.Error)
		if IsJSONOutput() {
			switch out.ErrorCode {
			case robot.ErrCodeSessionNotFound, robot.ErrCodePaneNotFound, "INVALID_SELECTION":
				return emitJSONFailureEnvelopeWithCause(output.NewErrorWithCode(out.ErrorCode, out.Error), cause)
			default:
				return emitJSONFailureEnvelopeWithCause(output.NewError(out.Error), cause)
			}
		}
		return cause
	}

	allBlocks := out.Blocks
	source := out.Source

	// Output
	if IsJSONOutput() {
		response := ExtractResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             out.Session,
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

blockLoop:
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

		// Check for risky paths — use cleanPath for all subsequent operations
		cleanPath := filepath.Clean(block.FilePath)
		isRisky := filepath.IsAbs(cleanPath) || strings.HasPrefix(cleanPath, "..") || strings.Contains(cleanPath, "/..")

		fmt.Printf("[%d] %s %s (%s)\n", i+1, action, cleanPath, langDisplay)

		if isRisky {
			fmt.Printf("    %s Blocked: path escapes current directory\n", "\033[31m✗\033[0m")
			skipped++
			continue
		}

		// Check if file exists
		_, err := os.Stat(cleanPath)
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
			// Ensure parent directory exists for nested paths
			if dir := filepath.Dir(cleanPath); dir != "." && dir != "" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					fmt.Printf("    ✗ Failed to create directory: %v\n", err)
					continue
				}
			}
			// Write the file (use cleanPath, not the raw block.FilePath)
			if err := os.WriteFile(cleanPath, []byte(block.Content), 0644); err != nil {
				fmt.Printf("    ✗ Failed to write: %v\n", err)
				continue
			}
			fmt.Printf("    ✓ Applied to %s\n", cleanPath)
			applied++
		case "s", "skip":
			fmt.Printf("Skipping remaining blocks\n")
			break blockLoop
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
