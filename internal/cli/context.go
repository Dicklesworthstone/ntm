package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
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
		newContextStatusCmd(),
		newContextRecoverCmd(),
	)

	return cmd
}

func newContextBuildCmd() *cobra.Command {
	var (
		beadID        string
		agentType     string
		model         string
		contextWindow int
		maxTokens     int
		task          string
		files         []string
	)

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build a context pack for a task",
		Long: `Build a context pack containing:
- BV triage data (priority and planning)
- CM rules (learned guidelines)
- CASS history (prior solutions)
- Native, project-confined file context

Use --model to size the pack for a specific model, --context-window to
supply its actual window, and --max-tokens to impose a smaller pack limit.
These are standalone preparation limits; delivery checks each target again.

			The context is rendered in agent-appropriate format:
			- Claude (cc), Cursor, Windsurf, Aider: XML format
			- Codex (cod), Gemini (gmi): Markdown format`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			if tmux.DefaultClient.Remote != "" {
				return ntmctx.ErrRemoteContextPreparation
			}
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
				Model:           model,
				ContextWindow:   contextWindow,
				MaxTokens:       maxTokens,
				RepoRev:         repoRev,
				Task:            task,
				Files:           files,
				ProjectDir:      dir,
				SessionID:       session,
				Remote:          tmux.DefaultClient.Remote != "",
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
			fmt.Printf("Token Budget: %d (estimated)\n", pack.TokenBudget)
			if pack.Model != "" {
				fmt.Printf("Model:        %s\n", pack.Model)
			}
			if pack.ContextWindow > 0 {
				fmt.Printf("Window:       %d tokens\n", pack.ContextWindow)
			}
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
	cmd.Flags().StringVar(&model, "model", "", "Model identifier for the context pack budget")
	cmd.Flags().IntVar(&contextWindow, "context-window", 0, "Actual model context window in tokens (0 uses the registry)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0, "Maximum estimated tokens in the complete pack (0 uses the model budget)")
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
	ErrorCode     string                `json:"error_code,omitempty"`
	Hint          string                `json:"hint,omitempty"`
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
		// The robot flag historically addresses a pane index, even when
		// several windows share it. Translate every match to an exact
		// address and let the canonical resolver enforce singularity.
		selectors := make([]string, 0, 1)
		for _, pane := range panes {
			if pane.Index == paneIdx {
				selectors = append(selectors, tmux.PaneTargetKey(pane, true))
			}
		}
		if len(selectors) == 0 {
			return nil, fmt.Errorf("pane %d not found in session %s", paneIdx, session)
		}
		selected, err := tmux.ResolvePaneSelectors(panes, selectors, true)
		if err != nil {
			return nil, fmt.Errorf("pane index %d in session %s: %w", paneIdx, session, err)
		}
		return selected, nil
	}
	return selectContextInjectionPanes(panes, "", targetAll, session)
}

// Shell injection shares send's topology-aware selector grammar: a numeric
// selector means a pane in one window or a window in a multi-window session.
// A singular injection refuses a window containing more than one pane.
func selectContextInjectionPanes(panes []tmux.Pane, selector string, targetAll bool, session string) ([]tmux.Pane, error) {
	if selector != "" {
		selected, err := tmux.ResolvePaneSelectors(panes, []string{selector}, true)
		if err != nil {
			return nil, fmt.Errorf("context injection in session %s: %w", session, err)
		}
		return selected, nil
	}
	if targetAll {
		return panes, nil
	}

	targets := make([]tmux.Pane, 0, len(panes))
	for _, p := range panes {
		typ := agent.AgentType(p.Type).Canonical()
		if typ.IsValid() && typ != agent.AgentTypeUser {
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no target panes for context injection in session %s: no known agents; select a pane explicitly or include all panes", session)
	}
	return targets, nil
}

// injectContextIntoPanes adapts the uniform-content robot injection path to
// the same batch dispatcher and safe receipts used by context inject.
func injectContextIntoPanes(ctx context.Context, session string, panes []tmux.Pane, content string, maxBytes int, dryRun bool, send contextPaneSender) (ContextInjectResult, error) {
	requests := uniformContextRequests(panes, content)
	for i := range requests {
		requests[i].MaxBytes = maxBytes
	}
	receipts, err := dispatchContextRequests(ctx, session, requests, dryRun, send)
	return contextInjectionResult(session, nil, len(content), false, dryRun, receipts, err), err
}

// formatContextInjectContent reads project-confined regular text files. A positive
// maxBytes bounds the complete message, including separators and truncation text;
// zero preserves the unlimited mode. Bounded reads inspect at most one extra
// byte to distinguish an exact fit from a truncated file.
func formatContextInjectContent(projectDir string, files []string, maxBytes int) (string, []string, bool, error) {
	if maxBytes < 0 {
		return "", nil, false, fmt.Errorf("maxBytes must be >= 0, got %d", maxBytes)
	}
	baseDir, err := filepath.Abs(projectDir)
	if err != nil {
		return "", nil, false, fmt.Errorf("resolve project directory: %w", err)
	}
	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return "", nil, false, fmt.Errorf("open project directory: %w", err)
	}
	defer root.Close()

	const separator = "\n\n---\n\n"
	const marker = "\n\n...(truncated)"
	var parts []string
	var injected []string
	totalSize := 0
	truncated := false

	for _, f := range files {
		if strings.TrimSpace(f) == "" {
			continue
		}
		_, displayPath, err := resolveContextInjectPath(baseDir, f)
		if err != nil {
			return "", nil, false, err
		}
		if !utf8.ValidString(displayPath) || strings.ContainsAny(displayPath, "\x00\r\n") {
			return "", nil, false, fmt.Errorf("inject file %q has an invalid text path", f)
		}
		header := fmt.Sprintf("### %s\n\n", displayPath)
		overhead := len(header)
		if len(parts) > 0 {
			overhead += len(separator)
		}
		remaining := -1 // An unlimited read is distinct from a zero-byte budget.
		if maxBytes > 0 {
			remaining = max(0, maxBytes-totalSize-overhead)
		}
		content, clipped, err := readContextInjectFile(root, filepath.FromSlash(displayPath), remaining)
		if err != nil {
			if os.IsNotExist(err) {
				continue // Skip missing files silently
			}
			return "", nil, false, fmt.Errorf("read %s: %w", f, err)
		}
		if content == "" && !clipped {
			continue
		}

		if clipped {
			truncated = true
			if remaining < len(marker) {
				break
			}
			limit := remaining - len(marker)
			if len(content) > limit {
				content = content[:limit]
				for !utf8.ValidString(content) {
					content = content[:len(content)-1]
				}
			}
			content += marker
		}

		parts = append(parts, header+content)
		injected = append(injected, displayPath)
		totalSize += overhead + len(content)

		if clipped {
			break
		}
	}

	return strings.Join(parts, separator), injected, truncated, nil
}

func readContextInjectFile(root *os.Root, name string, limit int) (string, bool, error) {
	// Stat before opening prevents ordinary FIFO/device selections from blocking.
	// Root confines both checks and the open, including symlink resolution.
	info, err := root.Stat(name)
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("not a regular file")
	}
	return readContextInjectText(file, limit)
}

// Reject binary and malformed text instead of sending replacement characters or
// terminal NULs. A bounded read may end inside a valid rune; only that incomplete
// suffix is discarded. Unread content is never claimed to have been validated.
func readContextInjectText(reader io.Reader, limit int) (string, bool, error) {
	source := reader
	if limit >= 0 {
		source = io.LimitReader(reader, int64(limit))
	}
	data, err := io.ReadAll(source)
	if err != nil {
		return "", false, err
	}
	clipped := false
	if limit >= 0 && len(data) == limit {
		var extra [1]byte
		n, err := io.ReadFull(reader, extra[:])
		if err != nil && err != io.EOF {
			return "", false, err
		}
		clipped = n > 0
	}
	if !utf8.Valid(data) {
		for i := 0; i < len(data); {
			r, size := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && size == 1 {
				if clipped && !utf8.FullRune(data[i:]) {
					data = data[:i]
					break
				}
				return "", false, fmt.Errorf("invalid UTF-8 text")
			}
			i += size
		}
	}
	text := string(data)
	if strings.ContainsRune(text, 0) {
		return "", false, fmt.Errorf("binary content contains NUL bytes")
	}
	return strings.TrimSpace(text), clipped, nil
}

func newContextInjectCmd() *cobra.Command {
	return newContextInjectCmdWithDeps(nil)
}

// newContextInjectCmdWithDeps keeps the real Cobra surface testable without
// a live tmux server or context providers. Production uses the same handler.
func newContextInjectCmdWithDeps(deps *contextInjectDeps) *cobra.Command {
	opts := contextInjectOptions{}

	cmd := &cobra.Command{
		Use:   "inject <session>",
		Short: "Inject project files or agent-specific context packs",
		Long: `Read AGENTS.md, README.md, and .claude/project_context.md from the
project directory and send their contents to agent panes.

Default files (skipped if missing): AGENTS.md, README.md, .claude/project_context.md

Use --files to override the file list.
By default, target every known agent, including agents at pane index zero.
Use --pane W.P or --pane %N for an exact target. A bare N selects pane N
in a single-window session or window N in a multi-window session; the selected
window must contain exactly one pane. Use --all to include shell panes in
file mode; context packs require agent targets.
Use --build to assemble native source, BV triage, CM rules, and CASS history
for each distinct target model, context window, and remaining pack budget.
Fresh process-bound accounting limits the pack to a fraction of remaining
space. Every target is checked again before any send. Unknown or stale usage,
including remote occupancy, is reported with an unverified agent-default
limit; it is never presented as an empty session or a measured fit.
These on-demand packs are transient; use context build to store an artifact.
Use --pack ID to send a previously built artifact to matching agent types.
Pack modes never truncate a rendered artifact; --max-bytes and measured
capacity reject oversized packs. --dry-run validates but sends no keys.
All modes apply the configured --redact policy to each complete outbound
message before any pane is written. Redaction that exceeds a byte or context
budget is rejected; stored packs are never modified.
Failed sends may have partially written input: inspect uncertain receipts
before retrying, rather than replaying the entire batch.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Session = strings.TrimSpace(args[0])
			opts.FilesSet = cmd.Flags().Changed("files")
			opts.PackSet = cmd.Flags().Changed("pack")
			opts.PaneSet = cmd.Flags().Changed("pane")
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
				if receipt.Capacity != nil {
					fmt.Fprintf(w, ", capacity=%s, pack-limit=%d estimated tokens", receipt.Capacity.Status, receipt.Capacity.BudgetTokens)
				}
				fmt.Fprintln(w)
			}
			for _, warning := range result.Warnings {
				fmt.Fprintf(w, "  Warning: %s\n", warning)
			}
			if result.Truncated {
				fmt.Fprintln(w, "  Content was truncated to fit its budget.")
			}
			if result.Hint != "" {
				fmt.Fprintf(w, "  Hint: %s\n", result.Hint)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&opts.FilesArg, "files", "", "Comma-separated project files (--build also accepts globs)")
	cmd.Flags().IntVar(&opts.MaxBytes, "max-bytes", 0, "Per-message byte limit (pack modes reject rather than truncate)")
	cmd.Flags().BoolVar(&opts.All, "all", false, "Select all panes (pack modes require every target to be an agent)")
	cmd.Flags().StringVar(&opts.Pane, "pane", "", "Inject to one target: N (pane/window), W.P, or %N (pane ID)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Prepare and validate without sending")
	cmd.Flags().BoolVar(&opts.Build, "build", false, "Build context for each target model and remaining capacity")
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
		usage: func(ctx context.Context, pane tmux.Pane) (*ntmctx.TranscriptUsage, bool) {
			if tmux.DefaultClient.Remote != "" || pane.PID <= 0 {
				return nil, false
			}
			return ntmctx.ProcessTranscriptUsage(ctx, string(agent.AgentType(pane.Type).Canonical()), pane.PID)
		},
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
		remote:    tmux.DefaultClient.Remote != "",
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

type contextStatusOptions struct {
	Session         string
	Pane            string
	WarnThreshold   float64
	RotateThreshold float64
}

type contextStatusDeps struct {
	resolve  func(ctx context.Context, name string) (string, error)
	panes    func(session string) ([]tmux.Pane, error)
	capture  func(ctx context.Context, target string, lines int) (string, error)
	usage    func(ctx context.Context, pane tmux.Pane) (*ntmctx.TranscriptUsage, bool)
	detector statuspkg.Detector
}

// ContextAgentStatus reports live context estimation, forecast, handoff advice,
// and runtime state for a single agent pane.
type ContextAgentStatus struct {
	AgentID      string                        `json:"agent_id"`
	PaneID       string                        `json:"pane_id"`
	PaneIndex    int                           `json:"pane_index"`
	AgentType    string                        `json:"agent_type"`
	Model        string                        `json:"model,omitempty"`
	State        string                        `json:"state,omitempty"`
	IdleSeconds  int64                         `json:"idle_seconds,omitempty"`
	ErrorMessage string                        `json:"error_message,omitempty"`
	Estimate     *ntmctx.ContextEstimate       `json:"estimate,omitempty"`
	Handoff      *ntmctx.HandoffRecommendation `json:"handoff,omitempty"`
}

// ContextStatusResult is the structured output for `ntm context status`.
type ContextStatusResult struct {
	Success        bool                      `json:"success"`
	Session        string                    `json:"session"`
	MonitoredCount int                       `json:"monitored_count"`
	WarnThreshold  float64                   `json:"warn_threshold"`
	RotateAt       float64                   `json:"rotate_threshold"`
	Agents         []ContextAgentStatus      `json:"agents"`
	AboveThreshold []ntmctx.AgentContextInfo `json:"above_threshold"`
}

func newContextStatusCmd() *cobra.Command {
	return newContextStatusCmdWithDeps(nil)
}

func newContextStatusCmdWithDeps(deps *contextStatusDeps) *cobra.Command {
	opts := contextStatusOptions{
		WarnThreshold:   70.0,
		RotateThreshold: 75.0,
	}

	cmd := &cobra.Command{
		Use:   "status [session]",
		Short: "Inspect live agent context window estimates, velocity forecasts, and handoff readiness",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.Session = strings.TrimSpace(args[0])
			}
			active := deps
			if active == nil {
				defaults := defaultContextStatusDeps(cmd.OutOrStdout())
				active = &defaults
			}
			result, err := runContextStatus(cmd.Context(), opts, *active)
			if err != nil {
				return err
			}
			if IsJSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			renderContextStatusText(cmd.OutOrStdout(), result)
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.Pane, "pane", "", "Inspect a single pane selector (N, W.P, or %N)")
	cmd.Flags().Float64Var(&opts.WarnThreshold, "warn-threshold", 70.0, "Context usage warning percentage")
	cmd.Flags().Float64Var(&opts.RotateThreshold, "rotate-threshold", 75.0, "Context usage rotation percentage")

	return cmd
}

func defaultContextStatusDeps(w io.Writer) contextStatusDeps {
	return contextStatusDeps{
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
		panes:   tmux.GetPanes,
		capture: tmux.CapturePaneOutputContext,
		usage: func(ctx context.Context, pane tmux.Pane) (*ntmctx.TranscriptUsage, bool) {
			if tmux.DefaultClient.Remote != "" || pane.PID <= 0 {
				return nil, false
			}
			return ntmctx.ProcessTranscriptUsage(ctx, string(agent.AgentType(pane.Type).Canonical()), pane.PID)
		},
		detector: statuspkg.NewDetector(),
	}
}

func runContextStatus(ctx context.Context, opts contextStatusOptions, deps contextStatusDeps) (ContextStatusResult, error) {
	if err := ctx.Err(); err != nil {
		return ContextStatusResult{}, err
	}
	session, err := deps.resolve(ctx, opts.Session)
	if err != nil {
		return ContextStatusResult{}, err
	}
	panes, err := deps.panes(session)
	if err != nil {
		return ContextStatusResult{}, err
	}
	selected, err := selectContextInjectionPanes(panes, opts.Pane, false, session)
	if err != nil {
		return ContextStatusResult{}, err
	}

	statusByPaneID := make(map[string]statuspkg.AgentStatus)
	statusByName := make(map[string]statuspkg.AgentStatus)
	if deps.detector != nil {
		if detected, detectErr := deps.detector.DetectAll(session); detectErr == nil {
			for _, st := range detected {
				if st.PaneID != "" {
					statusByPaneID[st.PaneID] = st
				}
				if st.PaneName != "" {
					statusByName[st.PaneName] = st
				}
			}
		}
	}

	monitor := ntmctx.NewContextMonitor(ntmctx.MonitorConfig{
		WarningThreshold: opts.WarnThreshold,
		RotateThreshold:  opts.RotateThreshold,
	})

	sort.Slice(selected, func(i, j int) bool {
		if selected[i].WindowIndex != selected[j].WindowIndex {
			return selected[i].WindowIndex < selected[j].WindowIndex
		}
		return selected[i].Index < selected[j].Index
	})

	type paneRef struct {
		agentID   string
		agentType string
		model     string
		pane      tmux.Pane
	}
	refs := make([]paneRef, 0, len(selected))

	for _, pane := range selected {
		agentID := strings.TrimSpace(pane.Title)
		if agentID == "" {
			agentID = fmt.Sprintf("%s_pane_%d", session, pane.Index)
		}
		agentType := string(agent.AgentType(pane.Type).Canonical())
		model := strings.TrimSpace(pane.Variant)

		var transcriptPath string
		var transcriptUsage *ntmctx.TranscriptUsage
		if deps.usage != nil {
			if u, ok := deps.usage(ctx, pane); ok && u != nil {
				transcriptUsage = u
				transcriptPath = u.Path
				if model == "" && u.Model != "" {
					model = u.Model
				}
			}
		}

		monitor.RegisterAgentWithTranscript(agentID, pane.ID, model, agentType, session, transcriptPath)

		var captured string
		if deps.capture != nil && pane.ID != "" {
			captured, _ = deps.capture(ctx, pane.ID, 60)
		}

		if captured != "" && ntmctx.ParseRobotModeContext(captured) != nil {
			monitor.UpdateFromRobotMode(agentID, captured)
		} else if transcriptUsage != nil && (transcriptUsage.InputTokens > 0 || transcriptUsage.OutputTokens > 0 || transcriptUsage.Tokens > 0) {
			inTokens := int64(transcriptUsage.InputTokens + transcriptUsage.CacheReadTokens + transcriptUsage.CacheCreationTokens)
			outTokens := int64(transcriptUsage.OutputTokens)
			if inTokens == 0 && outTokens == 0 && transcriptUsage.Tokens > 0 {
				inTokens = int64(transcriptUsage.Tokens)
			}
			monitor.RecordMessage(agentID, inTokens, outTokens)
		} else if transcriptPath != "" {
			_, _ = monitor.UpdateFromTranscript(agentID)
		}

		refs = append(refs, paneRef{
			agentID:   agentID,
			agentType: agentType,
			model:     model,
			pane:      pane,
		})
	}

	allEstimates := monitor.GetAllEstimates()
	aboveThreshold := monitor.AgentsAboveThreshold(opts.WarnThreshold)
	if aboveThreshold == nil {
		aboveThreshold = []ntmctx.AgentContextInfo{}
	}

	agents := make([]ContextAgentStatus, 0, len(refs))
	for _, ref := range refs {
		entry := ContextAgentStatus{
			AgentID:   ref.agentID,
			PaneID:    ref.pane.ID,
			PaneIndex: ref.pane.Index,
			AgentType: ref.agentType,
			Model:     ref.model,
			Estimate:  allEstimates[ref.agentID],
			Handoff:   monitor.ShouldTriggerHandoff(ref.agentID, nil),
		}
		if st, ok := statusByPaneID[ref.pane.ID]; ok {
			entry.State = string(st.State)
			entry.IdleSeconds = int64(st.IdleDuration().Seconds())
			if st.ErrorType != statuspkg.ErrorNone {
				entry.ErrorMessage = st.ErrorType.Message()
			}
		} else if st, ok := statusByName[ref.agentID]; ok {
			entry.State = string(st.State)
			entry.IdleSeconds = int64(st.IdleDuration().Seconds())
			if st.ErrorType != statuspkg.ErrorNone {
				entry.ErrorMessage = st.ErrorType.Message()
			}
		}
		agents = append(agents, entry)
	}

	return ContextStatusResult{
		Success:        true,
		Session:        session,
		MonitoredCount: monitor.Count(),
		WarnThreshold:  opts.WarnThreshold,
		RotateAt:       opts.RotateThreshold,
		Agents:         agents,
		AboveThreshold: aboveThreshold,
	}, nil
}

func renderContextStatusText(w io.Writer, result ContextStatusResult) {
	fmt.Fprintf(w, "Context Status: session=%s monitored=%d above-warn=%d\n",
		result.Session, result.MonitoredCount, len(result.AboveThreshold))
	for _, a := range result.Agents {
		fmt.Fprintf(w, "  Pane %d (%s) [%s]: ", a.PaneIndex, a.PaneID, a.AgentID)
		if a.Estimate != nil {
			fmt.Fprintf(w, "%.1f%% (%d/%d tokens, %s)",
				a.Estimate.UsagePercent, a.Estimate.TokensUsed, a.Estimate.ContextLimit, a.Estimate.Method)
			if a.Estimate.TokenVelocity > 0 {
				fmt.Fprintf(w, ", velocity=%.0f tpm", a.Estimate.TokenVelocity)
				if a.Estimate.VelocityTrend != "" {
					fmt.Fprintf(w, " (%s)", a.Estimate.VelocityTrend)
				}
				if a.Estimate.MinutesToExhaustion > 0 {
					fmt.Fprintf(w, ", eta=%.1fm", a.Estimate.MinutesToExhaustion)
				}
			}
		} else {
			fmt.Fprint(w, "no context estimate yet")
		}
		if a.State != "" {
			fmt.Fprintf(w, ", state=%s", a.State)
			if a.IdleSeconds > 0 {
				fmt.Fprintf(w, " (idle %ds)", a.IdleSeconds)
			}
		}
		if a.ErrorMessage != "" {
			fmt.Fprintf(w, ", error=%q", a.ErrorMessage)
		}
		if a.Handoff != nil && (a.Handoff.ShouldTrigger || a.Handoff.ShouldWarn) {
			fmt.Fprintf(w, " — %s", a.Handoff.Reason)
		}
		fmt.Fprintln(w)
	}
}

type contextRecoverOptions struct {
	Session  string
	Pane     int
	PaneID   string
	Prompt   string
	Cooldown time.Duration
	NoBeads  bool
}

type contextRecoverDeps struct {
	resolve func(ctx context.Context, name string) (string, error)
	panes   func(session string) ([]tmux.Pane, error)
	manager *statuspkg.RecoveryManager
}

// ContextRecoverPaneOutcome reports the recovery prompt outcome for a single pane.
type ContextRecoverPaneOutcome struct {
	PaneID         string `json:"pane_id"`
	PaneIndex      int    `json:"pane_index"`
	Sent           bool   `json:"sent"`
	SkippedReason  string `json:"skipped_reason,omitempty"`
	Error          string `json:"error,omitempty"`
	RecoveryCount  int    `json:"recovery_count"`
	LastRecoveryAt string `json:"last_recovery_at,omitempty"`
}

// ContextRecoverResult is the structured output for `ntm context recover`.
type ContextRecoverResult struct {
	Success  bool                        `json:"success"`
	Session  string                      `json:"session"`
	Outcomes []ContextRecoverPaneOutcome `json:"outcomes"`
	Events   []statuspkg.RecoveryEvent   `json:"events"`
}

func newContextRecoverCmd() *cobra.Command {
	return newContextRecoverCmdWithDeps(nil)
}

func newContextRecoverCmdWithDeps(deps *contextRecoverDeps) *cobra.Command {
	opts := contextRecoverOptions{Pane: -1}

	cmd := &cobra.Command{
		Use:   "recover <session>",
		Short: "Send a context-aware post-compaction recovery prompt to agent panes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Session = strings.TrimSpace(args[0])
			active := deps
			if active == nil {
				defaults := defaultContextRecoverDeps(cmd.OutOrStdout())
				active = &defaults
			}
			result, err := runContextRecover(cmd.Context(), opts, *active)
			if IsJSONOutput() {
				if encErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encErr != nil {
					return encErr
				}
				if err != nil {
					return jsonFailureExit()
				}
				return nil
			}
			for _, outcome := range result.Outcomes {
				switch {
				case outcome.Sent:
					fmt.Fprintf(cmd.OutOrStdout(), "✓ Sent recovery prompt to %s (count=%d)\n", outcome.PaneID, outcome.RecoveryCount)
				case outcome.SkippedReason != "":
					fmt.Fprintf(cmd.OutOrStdout(), "- Skipped %s: %s\n", outcome.PaneID, outcome.SkippedReason)
				case outcome.Error != "":
					fmt.Fprintf(cmd.OutOrStdout(), "✗ Failed %s: %s\n", outcome.PaneID, outcome.Error)
				}
			}
			return err
		},
	}

	cmd.Flags().IntVar(&opts.Pane, "pane", -1, "Target pane index (sends to all agent panes when omitted)")
	cmd.Flags().StringVar(&opts.PaneID, "pane-id", "", "Durable tmux pane ID (e.g. %17)")
	cmd.Flags().StringVar(&opts.Prompt, "prompt", "", "Custom recovery prompt override")
	cmd.Flags().DurationVar(&opts.Cooldown, "cooldown", 0, "Per-pane recovery cooldown override")
	cmd.Flags().BoolVar(&opts.NoBeads, "no-beads", false, "Omit bead triage context from recovery prompt")

	return cmd
}

func defaultContextRecoverDeps(w io.Writer) contextRecoverDeps {
	return contextRecoverDeps{
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
		panes: tmux.GetPanes,
	}
}

func runContextRecover(ctx context.Context, opts contextRecoverOptions, deps contextRecoverDeps) (ContextRecoverResult, error) {
	if err := ctx.Err(); err != nil {
		return ContextRecoverResult{}, err
	}
	session, err := deps.resolve(ctx, opts.Session)
	if err != nil {
		return ContextRecoverResult{}, err
	}

	rm := deps.manager
	if rm == nil {
		rcfg := statuspkg.DefaultRecoveryConfig()
		rcfg.IncludeBeadContext = !opts.NoBeads
		rm = statuspkg.NewRecoveryManager(rcfg)
	}
	if opts.Prompt != "" {
		rm.SetPrompt(opts.Prompt)
	}
	if opts.Cooldown > 0 {
		rm.SetCooldown(opts.Cooldown)
	}

	var outcomes []ContextRecoverPaneOutcome
	var firstErr error

	recordOutcome := func(paneKey string, paneIdx int, sent bool, skipReason string, sendErr error) {
		out := ContextRecoverPaneOutcome{
			PaneID:        paneKey,
			PaneIndex:     paneIdx,
			Sent:          sent,
			SkippedReason: skipReason,
			RecoveryCount: rm.GetRecoveryCount(paneKey),
		}
		if sendErr != nil {
			out.Error = sendErr.Error()
			if firstErr == nil {
				firstErr = sendErr
			}
		}
		if last, ok := rm.GetLastRecoveryTime(paneKey); ok {
			out.LastRecoveryAt = last.UTC().Format(time.RFC3339)
		}
		outcomes = append(outcomes, out)
	}

	if opts.Pane >= 0 && strings.TrimSpace(opts.PaneID) == "" {
		paneKey := fmt.Sprintf("%s:.%d", session, opts.Pane)
		if can, reason := rm.CanSendRecovery(paneKey); !can {
			recordOutcome(paneKey, opts.Pane, false, reason, nil)
		} else {
			sent, sendErr := rm.SendRecoveryPrompt(session, opts.Pane)
			recordOutcome(paneKey, opts.Pane, sent, "", sendErr)
		}
	} else {
		panes, panesErr := deps.panes(session)
		if panesErr != nil {
			return ContextRecoverResult{Session: session}, panesErr
		}
		var targets []tmux.Pane
		if paneID := strings.TrimSpace(opts.PaneID); paneID != "" {
			for _, p := range panes {
				if p.ID == paneID {
					targets = append(targets, p)
					break
				}
			}
			if len(targets) == 0 {
				return ContextRecoverResult{Session: session}, fmt.Errorf("pane %s not found in session %s", paneID, session)
			}
		} else {
			selected, selErr := selectContextInjectionPanes(panes, "", false, session)
			if selErr != nil {
				return ContextRecoverResult{Session: session}, selErr
			}
			targets = selected
		}
		for _, p := range targets {
			if can, reason := rm.CanSendRecovery(p.ID); !can {
				recordOutcome(p.ID, p.Index, false, reason, nil)
				continue
			}
			sent, sendErr := rm.SendRecoveryPromptByID(session, p.Index, p.ID, "manual")
			recordOutcome(p.ID, p.Index, sent, "", sendErr)
		}
	}

	events := rm.GetRecoveryEvents()
	if events == nil {
		events = []statuspkg.RecoveryEvent{}
	}

	return ContextRecoverResult{
		Success:  firstErr == nil,
		Session:  session,
		Outcomes: outcomes,
		Events:   events,
	}, firstErr
}
