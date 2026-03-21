package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Manage context packs for agent tasks",
	}

	cmd.AddCommand(
		newContextBuildCmd(),
		newContextShowCmd(),
		newContextStatsCmd(),
		newContextClearCmd(),
		newContextInjectCmd(),
	)

	return cmd
}

func newContextBuildCmd() *cobra.Command {
	var (
		beadID    string
		agentType string
		task      string
		files     []string
	)

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build a context pack for a task",
		Long: `Build a context pack containing:
- BV triage data (priority and planning)
- CM rules (learned guidelines)
- CASS history (prior solutions)
- S2P file context

The context is rendered in agent-appropriate format:
- Claude (cc), Cursor, Windsurf, Aider: XML format
- Codex (cod), Gemini (gmi): Markdown format`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, _ := os.Getwd()

			// Get repo revision
			repoRev := getRepoRev(dir)

			// Get session info
			session := tmux.GetCurrentSession()
			if session == "" {
				session = filepath.Base(dir)
			}

			// Open state store
			store, err := state.Open("")
			if err != nil {
				return fmt.Errorf("open state store: %w", err)
			}
			defer store.Close()

			if err := store.Migrate(); err != nil {
				return fmt.Errorf("migrate state store: %w", err)
			}

			// Build context pack
			builder := ntmctx.NewContextPackBuilder(store)

			opts := ntmctx.BuildOptions{
				BeadID:          beadID,
				AgentType:       agentType,
				RepoRev:         repoRev,
				Task:            task,
				Files:           files,
				ProjectDir:      dir,
				SessionID:       session,
				IncludeMSSkills: cfg != nil && cfg.Context.MSSkills,
			}

			pack, err := builder.Build(cmd.Context(), opts)
			if err != nil {
				return err
			}

			if IsJSONOutput() {
				return output.PrintJSON(pack)
			}

			// Print summary
			fmt.Printf("Context Pack: %s\n", pack.ID)
			fmt.Printf("Agent Type:   %s\n", pack.AgentType)
			fmt.Printf("Token Count:  %d\n", pack.TokenCount)
			fmt.Println()

			for name, comp := range pack.Components {
				status := "✓"
				if comp.Error != "" {
					status = "✗ " + comp.Error
				}
				fmt.Printf("  %s: %s (%d tokens)\n", name, status, comp.TokenCount)
			}
			fmt.Println()

			// Print rendered prompt if verbose
			verbose, _ := cmd.Flags().GetBool("verbose")
			if verbose {
				fmt.Println("--- Rendered Prompt ---")
				fmt.Println(pack.RenderedPrompt)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&beadID, "bead", "", "Bead ID for context")
	cmd.Flags().StringVar(&agentType, "agent", "cc", "Agent type (cc, cod, gmi, cursor, windsurf, aider)")
	cmd.Flags().StringVar(&task, "task", "", "Task description for CM context")
	cmd.Flags().StringSliceVar(&files, "files", nil, "Files to include in S2P context")
	cmd.Flags().Bool("verbose", false, "Show full rendered prompt")

	return cmd
}

func newContextShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <pack-id>",
		Short: "Show a stored context pack",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			packID := args[0]

			store, err := state.Open("")
			if err != nil {
				return fmt.Errorf("open state store: %w", err)
			}
			defer store.Close()

			pack, err := store.GetContextPack(packID)
			if err != nil {
				return fmt.Errorf("get context pack: %w", err)
			}

			if pack == nil {
				return fmt.Errorf("context pack not found: %s", packID)
			}

			if IsJSONOutput() {
				return output.PrintJSON(pack)
			}

			fmt.Printf("ID:           %s\n", pack.ID)
			fmt.Printf("Bead ID:      %s\n", pack.BeadID)
			fmt.Printf("Agent Type:   %s\n", pack.AgentType)
			fmt.Printf("Repo Rev:     %s\n", pack.RepoRev)
			fmt.Printf("Created:      %s\n", pack.CreatedAt.Format("2006-01-02 15:04:05"))
			fmt.Printf("Token Count:  %d\n", pack.TokenCount)

			if pack.RenderedPrompt != "" {
				fmt.Println()
				fmt.Println("--- Rendered Prompt ---")
				fmt.Println(pack.RenderedPrompt)
			}

			return nil
		},
	}
}

func newContextStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show context pack cache statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Create builder to check cache stats
			builder := ntmctx.NewContextPackBuilder(nil)
			size, keys := builder.CacheStats()

			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"cache_size": size,
					"cache_keys": keys,
				})
			}

			fmt.Printf("Cache Size: %d entries\n", size)
			if size > 0 {
				fmt.Println("Cache Keys:")
				for _, k := range keys {
					fmt.Printf("  - %s\n", k)
				}
			}

			return nil
		},
	}
}

func newContextClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Clear the context pack cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			builder := ntmctx.NewContextPackBuilder(nil)
			builder.ClearCache()

			fmt.Println("Context pack cache cleared.")
			return nil
		},
	}
}

// ContextInjectResult is the JSON output for the context inject command.
type ContextInjectResult struct {
	Success       bool     `json:"success"`
	Session       string   `json:"session"`
	InjectedFiles []string `json:"injected_files"`
	TotalBytes    int      `json:"total_bytes"`
	Truncated     bool     `json:"truncated"`
	PanesInjected []int    `json:"panes_injected"`
	Error         string   `json:"error,omitempty"`
}

// defaultContextFiles returns the default files to inject.
func defaultContextFiles() []string {
	return []string{"AGENTS.md", "README.md", ".claude/project_context.md"}
}

func resolveContextInjectPath(projectDir, rawPath string) (string, string, error) {
	file := strings.TrimSpace(rawPath)
	if file == "" {
		return "", "", fmt.Errorf("inject file path cannot be empty")
	}

	cleaned := filepath.Clean(file)
	if cleaned == "." {
		return "", "", fmt.Errorf("inject file path %q is invalid", rawPath)
	}
	if filepath.IsAbs(cleaned) {
		return "", "", fmt.Errorf("inject file %q must be project-relative", rawPath)
	}

	joined := filepath.Join(projectDir, cleaned)
	rel, err := filepath.Rel(projectDir, joined)
	if err != nil {
		return "", "", fmt.Errorf("resolve inject file %q: %w", rawPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("inject file %q escapes project directory", rawPath)
	}

	return joined, filepath.ToSlash(rel), nil
}

func selectContextInjectTargetPanes(panes []tmux.Pane, paneIdx int, targetAll bool, session string) ([]tmux.Pane, error) {
	if paneIdx >= 0 {
		for _, p := range panes {
			if p.Index == paneIdx {
				return []tmux.Pane{p}, nil
			}
		}
		return nil, fmt.Errorf("pane %d not found in session %s", paneIdx, session)
	}
	if targetAll {
		return panes, nil
	}

	targets := make([]tmux.Pane, 0, len(panes))
	for _, p := range panes {
		if p.Index > 0 {
			targets = append(targets, p)
		}
	}
	return targets, nil
}

// formatContextInjectContent reads files and formats them for injection.
func formatContextInjectContent(projectDir string, files []string, maxBytes int) (string, []string, bool, error) {
	if maxBytes < 0 {
		return "", nil, false, fmt.Errorf("maxBytes must be >= 0, got %d", maxBytes)
	}
	baseDir, err := filepath.Abs(projectDir)
	if err != nil {
		return "", nil, false, fmt.Errorf("resolve project directory: %w", err)
	}

	var parts []string
	var injected []string
	totalSize := 0
	truncated := false

	for _, f := range files {
		if strings.TrimSpace(f) == "" {
			continue
		}
		path, displayPath, err := resolveContextInjectPath(baseDir, f)
		if err != nil {
			return "", nil, false, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // Skip missing files silently
			}
			return "", nil, false, fmt.Errorf("read %s: %w", f, err)
		}

		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}

		// Check if adding this file would exceed max bytes
		header := fmt.Sprintf("### %s\n\n", displayPath)
		entrySize := len(header) + len(content) + 2 // +2 for trailing newlines
		if maxBytes > 0 && totalSize+entrySize > maxBytes {
			// Truncate this file's content to fit
			remaining := maxBytes - totalSize - len(header) - len("\n\n...(truncated)\n")
			if remaining <= 0 {
				truncated = true
				break
			}
			content = content[:remaining] + "\n\n...(truncated)"
			truncated = true
		}

		parts = append(parts, header+content)
		injected = append(injected, displayPath)
		totalSize += len(header) + len(content) + 2

		if maxBytes > 0 && truncated {
			break
		}
	}

	if len(parts) == 0 {
		return "", nil, false, nil
	}

	result := strings.Join(parts, "\n\n---\n\n")
	return result, injected, truncated, nil
}

func newContextInjectCmd() *cobra.Command {
	var (
		filesArg  string
		maxBytes  int
		targetAll bool
		paneIdx   int
		dryRun    bool
	)

	cmd := &cobra.Command{
		Use:   "inject <session>",
		Short: "Inject project context files into agent panes",
		Long: `Read AGENTS.md, README.md, and .claude/project_context.md from the
project directory and send their contents to agent panes.

Default files (skipped if missing): AGENTS.md, README.md, .claude/project_context.md

Use --files to override the file list.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]

			// Resolve project directory
			projectDir := ""
			if cfg != nil {
				projectDir = cfg.GetProjectDir(session)
			}
			if projectDir == "" {
				dir, _ := os.Getwd()
				projectDir = dir
			}

			// Determine files to inject
			files := defaultContextFiles()
			if filesArg != "" {
				files = strings.Split(filesArg, ",")
				for i := range files {
					files[i] = strings.TrimSpace(files[i])
				}
			}

			// Build the injection content
			content, injected, truncated, err := formatContextInjectContent(projectDir, files, maxBytes)
			if err != nil {
				if IsJSONOutput() {
					return output.PrintJSON(ContextInjectResult{
						Success: false,
						Session: session,
						Error:   err.Error(),
					})
				}
				return err
			}

			if len(injected) == 0 {
				if IsJSONOutput() {
					return output.PrintJSON(ContextInjectResult{
						Success:       true,
						Session:       session,
						InjectedFiles: []string{},
						PanesInjected: []int{},
					})
				}
				fmt.Println("No context files found to inject.")
				return nil
			}

			// Get panes
			panes, err := tmux.GetPanes(session)
			if err != nil {
				if IsJSONOutput() {
					return output.PrintJSON(ContextInjectResult{
						Success: false,
						Session: session,
						Error:   fmt.Sprintf("get panes: %s", err),
					})
				}
				return fmt.Errorf("get panes: %w", err)
			}

			targetPanes, err := selectContextInjectTargetPanes(panes, paneIdx, targetAll, session)
			if err != nil {
				if IsJSONOutput() {
					return output.PrintJSON(ContextInjectResult{
						Success: false,
						Session: session,
						Error:   err.Error(),
					})
				}
				return err
			}

			// Track injected pane indices
			var injectedPanes []int

			if !dryRun {
				// Send content to target panes
				for _, p := range targetPanes {
					target := p.ID
					if err := tmux.SendKeys(target, content, true); err != nil {
						slog.Warn("failed to send context to pane",
							"session", session,
							"pane", p.Index,
							"error", err,
						)
						continue
					}
					injectedPanes = append(injectedPanes, p.Index)
				}
			} else {
				for _, p := range targetPanes {
					injectedPanes = append(injectedPanes, p.Index)
				}
			}

			result := ContextInjectResult{
				Success:       true,
				Session:       session,
				InjectedFiles: injected,
				TotalBytes:    len(content),
				Truncated:     truncated,
				PanesInjected: injectedPanes,
			}

			if IsJSONOutput() {
				return output.PrintJSON(result)
			}

			// Human-readable output
			if dryRun {
				fmt.Println("[dry-run] Would inject context:")
			} else {
				fmt.Println("Context injected:")
			}
			fmt.Printf("  Session: %s\n", session)
			fmt.Printf("  Files:   %s\n", strings.Join(injected, ", "))
			fmt.Printf("  Size:    %d bytes\n", len(content))
			if truncated {
				fmt.Println("  (content was truncated)")
			}
			fmt.Printf("  Panes:   %v\n", injectedPanes)

			return nil
		},
	}

	cmd.Flags().StringVar(&filesArg, "files", "", "Comma-separated list of files to inject (overrides defaults)")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", 0, "Maximum total content size in bytes (0 = unlimited)")
	cmd.Flags().BoolVar(&targetAll, "all", false, "Inject to all panes including user pane (default: agent panes only)")
	cmd.Flags().IntVar(&paneIdx, "pane", -1, "Inject to specific pane index only")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be injected without sending")

	return cmd
}

// getRepoRev returns the current git HEAD revision
func getRepoRev(dir string) string {
	gitDir, err := resolveGitDir(dir)
	if err != nil {
		return "unknown"
	}

	headPath := filepath.Join(gitDir, "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return "unknown"
	}

	head := strings.TrimSpace(string(data))
	if strings.HasPrefix(head, "ref: ") {
		ref := strings.TrimSpace(head[5:])
		if ref == "" {
			return "unknown"
		}
		rev, err := readGitRevision(gitDir, ref)
		if err == nil {
			return rev
		}
		return "unknown"
	}

	// Direct SHA
	if len(head) >= 40 {
		return head[:40]
	}

	return "unknown"
}

func resolveGitDir(dir string) (string, error) {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return gitPath, nil
	}

	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", err
	}

	content := strings.TrimSpace(string(data))
	const gitDirPrefix = "gitdir:"
	if !strings.HasPrefix(content, gitDirPrefix) {
		return "", fmt.Errorf("unsupported .git file format")
	}

	gitDir := strings.TrimSpace(strings.TrimPrefix(content, gitDirPrefix))
	if gitDir == "" {
		return "", fmt.Errorf("empty gitdir")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	return filepath.Clean(gitDir), nil
}

func readGitRevision(gitDir, ref string) (string, error) {
	refPath := filepath.Join(gitDir, filepath.FromSlash(ref))
	if refData, err := os.ReadFile(refPath); err == nil {
		rev := strings.TrimSpace(string(refData))
		if len(rev) > 40 {
			rev = rev[:40]
		}
		return rev, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	packedRefsPath := filepath.Join(gitDir, "packed-refs")
	packedRefs, err := os.ReadFile(packedRefsPath)
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(string(packedRefs), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != ref {
			continue
		}
		rev := fields[0]
		if len(rev) > 40 {
			rev = rev[:40]
		}
		return rev, nil
	}

	return "", fmt.Errorf("git ref %q not found", ref)
}
