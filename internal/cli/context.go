package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
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
- Native, project-confined file context

			The context is rendered in agent-appropriate format:
			- Claude (cc), Cursor, Windsurf, Aider: XML format
			- Codex (cod), Gemini (gmi): Markdown format`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, session, err := resolveContextBuildScope(cmd.Context(), tmux.GetCurrentSession())
			if err != nil {
				return err
			}

			// Get repo revision
			repoRev := getRepoRev(dir)

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
			builder := ntmctx.NewContextPackBuilder(nil)

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
			// Do not report a reusable ID when persistence failed. Cache hits
			// must also be saved in the currently selected state store.
			if err := persistContextPack(cmd.Context(), store, &pack.ContextPack); err != nil {
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
	cmd.Flags().StringSliceVar(&files, "files", nil, "Project-relative files or globs to include in native source context")
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

func resolveContextBuildScope(ctx context.Context, session string) (string, string, error) {
	session = strings.TrimSpace(session)
	if session != "" {
		resolved, err := normalizeProjectScopedSessionName(ctx, session, !IsJSONOutput())
		if err != nil {
			return "", "", err
		}
		session = resolved
		projectDir, err := resolveExplicitProjectDirForSessionContext(ctx, session)
		if err != nil {
			return "", "", err
		}
		return projectDir, session, nil
	}

	projectDir := GetProjectRoot()
	if projectDir == "" {
		return "", "", fmt.Errorf("getting project root failed")
	}
	return projectDir, defaultProjectScopedSession(projectDir), nil
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
	Success       bool                  `json:"success"`
	Session       string                `json:"session"`
	InjectedFiles []string              `json:"injected_files"`
	TotalBytes    int                   `json:"total_bytes"`
	Truncated     bool                  `json:"truncated"`
	PanesInjected []int                 `json:"panes_injected"`
	Error         string                `json:"error,omitempty"`
	DryRun        bool                  `json:"dry_run"`
	PanesPlanned  []int                 `json:"panes_planned,omitempty"`
	Deliveries    []ContextPaneDelivery `json:"deliveries,omitempty"`
	Mode          string                `json:"mode,omitempty"`
	SelectedFiles []string              `json:"selected_files,omitempty"`
	Warnings      []string              `json:"warnings,omitempty"`
}

func init() {
	robot.MustRegisterSchemaCommand("context_inject", ContextInjectResult{})
	robot.MustRegisterSchemaPagination("context_inject", robot.SchemaPaginationFlag{
		Reason: "bounded: per-request injection echo (files/panes for one inject)",
	})
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

type contextInjectSender func(target, content string, enter bool) error

// injectContextIntoPanes adapts the uniform-content robot injection path to
// the same batch dispatcher used by context inject. Its error includes the
// delivered pane IDs so a partial delivery must not be blindly replayed.
func injectContextIntoPanes(session string, panes []tmux.Pane, content string, dryRun bool, sender contextInjectSender) ([]int, error) {
	var send contextPaneSender
	if sender != nil {
		send = func(_ context.Context, pane tmux.Pane, text string) error {
			return sender(pane.ID, text, true)
		}
	}
	receipts, err := dispatchContextRequests(context.Background(), session, uniformContextRequests(panes, content), dryRun, send)
	injected := make([]int, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt.Status == "delivered" || (dryRun && receipt.Status == "planned") {
			injected = append(injected, receipt.Pane)
		}
	}
	return injected, err
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
	return newContextInjectCmdWithDeps(nil)
}

// newContextInjectCmdWithDeps keeps the real Cobra surface testable without
// a live tmux server or context providers. Production uses the same handler.
func newContextInjectCmdWithDeps(deps *contextInjectDeps) *cobra.Command {
	opts := contextInjectOptions{Pane: -1}

	cmd := &cobra.Command{
		Use:   "inject <session>",
		Short: "Inject project files or agent-specific context packs",
		Long: `Read AGENTS.md, README.md, and .claude/project_context.md from the
project directory and send their contents to agent panes.

Default files (skipped if missing): AGENTS.md, README.md, .claude/project_context.md

Use --files to override the file list.
Use --build to assemble native source, BV triage, CM rules, and CASS history
once per target agent type, with that agent's format and token budget.
These on-demand packs are transient; use context build to store an artifact.
Use --pack ID to send a previously built artifact to matching agent types.
Pack modes never truncate a rendered artifact; --max-bytes rejects an
oversized pack instead. --dry-run prepares and validates but sends no keys.
Failed sends may have partially written input: inspect uncertain receipts
before retrying, rather than replaying the entire batch.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Session = strings.TrimSpace(args[0])
			opts.FilesSet = cmd.Flags().Changed("files")
			opts.PackSet = cmd.Flags().Changed("pack")
			active := deps
			if active == nil {
				defaults := defaultContextInjectDeps(cmd.OutOrStdout())
				active = &defaults
			}
			result, err := runContextInjection(cmd.Context(), opts, *active)
			if IsJSONOutput() {
				if encErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encErr != nil {
					return encErr
				}
				if err != nil {
					return jsonFailureExit()
				}
				return nil
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Context injection: session=%s mode=%s dry-run=%t success=%t\n", result.Session, result.Mode, result.DryRun, result.Success)
			for _, receipt := range result.Deliveries {
				fmt.Fprintf(w, "  Pane %d (%s): %s, %d bytes", receipt.Pane, receipt.Target, receipt.Status, receipt.Bytes)
				if receipt.PackID != "" {
					fmt.Fprintf(w, ", pack=%s", receipt.PackID)
				}
				fmt.Fprintln(w)
			}
			for _, warning := range result.Warnings {
				fmt.Fprintf(w, "  Warning: %s\n", warning)
			}
			if result.Truncated {
				fmt.Fprintln(w, "  Content was truncated to fit its budget.")
			}
			return err
		},
	}

	cmd.Flags().StringVar(&opts.FilesArg, "files", "", "Comma-separated project files (--build also accepts globs)")
	cmd.Flags().IntVar(&opts.MaxBytes, "max-bytes", 0, "Per-message byte limit (pack modes reject rather than truncate)")
	cmd.Flags().BoolVar(&opts.All, "all", false, "Select all panes (pack modes require every target to be an agent)")
	cmd.Flags().IntVar(&opts.Pane, "pane", -1, "Inject to a specific pane index")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Prepare and validate without sending")
	cmd.Flags().BoolVar(&opts.Build, "build", false, "Build a fresh context pack for each target agent type")
	cmd.Flags().StringVar(&opts.PackID, "pack", "", "Inject a stored context pack into matching agent types")
	cmd.Flags().StringVar(&opts.Task, "task", "", "Task description for context retrieval (requires --build)")
	cmd.Flags().StringVar(&opts.BeadID, "bead", "", "Bead ID to include in built packs (requires --build)")

	return cmd
}

func defaultContextInjectDeps(w io.Writer) contextInjectDeps {
	builder := ntmctx.NewContextPackBuilder(nil)
	return contextInjectDeps{
		resolve: func(ctx context.Context, name string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			res, err := ResolveSessionWithOptions(name, w, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
			if err != nil {
				return "", err
			}
			return res.Session, nil
		},
		project: resolveExplicitProjectDirForSessionContext,
		panes:   tmux.GetPanes,
		build:   builder.Build,
		load: func(id string) (*state.ContextPack, error) {
			store, err := state.Open("")
			if err != nil {
				return nil, fmt.Errorf("open state store: %w", err)
			}
			defer store.Close()
			return store.GetContextPack(id)
		},
		send:      sendContextToPane,
		includeMS: cfg != nil && cfg.Context.MSSkills,
	}
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
