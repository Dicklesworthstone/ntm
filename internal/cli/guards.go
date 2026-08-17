package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func newGuardsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guards",
		Short: "Manage Agent Mail pre-commit guards",
		Long: `Manage Agent Mail pre-commit guards for multi-agent coordination.

Pre-commit guards prevent accidental commits during agent operations:
- Check for active file reservations before commit
- Validate no conflicting agent work is in progress
- Ensure coordination safety in multi-agent sessions

Use 'ntm guards install' to install the guard in the current repository.
Use 'ntm guards uninstall' to remove the guard.
Use 'ntm guards status' to check installation status.`,
	}

	cmd.AddCommand(
		newGuardsInstallCmd(),
		newGuardsUninstallCmd(),
		newGuardsStatusCmd(),
		newGuardsCheckCmd(),
	)

	return cmd
}

func newGuardsInstallCmd() *cobra.Command {
	var projectKey string
	var force bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install pre-commit guard in current repository",
		Long: `Install the Agent Mail pre-commit guard in the current git repository.

The guard prevents commits that conflict with active agent file reservations.
It integrates with the Agent Mail coordination system to ensure safe multi-agent
development.

The project key defaults to the current working directory. Use --project-key
to specify a different Agent Mail project.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGuardsInstall(projectKey, force)
		},
	}

	cmd.Flags().StringVarP(&projectKey, "project-key", "p", "", "Agent Mail project key (defaults to current directory)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing guard")

	return cmd
}

// GuardsInstallResponse is the JSON output for guards install.
type GuardsInstallResponse struct {
	output.TimestampedResponse
	Success    bool   `json:"success"`
	RepoPath   string `json:"repo_path"`
	ProjectKey string `json:"project_key"`
	HookPath   string `json:"hook_path"`
	Message    string `json:"message,omitempty"`
}

func runGuardsInstall(projectKey string, force bool) error {
	// Get current directory
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find git root
	repoPath, err := findGitRoot(cwd)
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	// Default project key to repo path
	if projectKey == "" {
		projectKey = repoPath
	}

	hookPath, err := findGitHookPath(repoPath, "pre-commit")
	if err != nil {
		return err
	}

	// Check if hook already exists
	if !force && fileExists(hookPath) {
		// Check if it's our hook
		content, err := os.ReadFile(hookPath)
		if err == nil && strings.Contains(string(content), "ntm-precommit-guard") {
			if IsJSONOutput() {
				return output.PrintJSON(GuardsInstallResponse{
					TimestampedResponse: output.NewTimestamped(),
					Success:             true,
					RepoPath:            repoPath,
					ProjectKey:          projectKey,
					HookPath:            hookPath,
					Message:             "Guard already installed",
				})
			}
			okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
			fmt.Println()
			fmt.Printf("  %s Guard already installed at %s\n", okStyle.Render("✓"), hookPath)
			fmt.Println()
			return nil
		}

		// It's a different hook
		return fmt.Errorf("pre-commit hook already exists at %s (use --force to overwrite)", hookPath)
	}

	// Try using Agent Mail MCP if available
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := agentmail.NewClient()
	err = client.InstallPrecommitGuard(ctx, projectKey, repoPath)
	if err == nil {
		if IsJSONOutput() {
			return output.PrintJSON(GuardsInstallResponse{
				TimestampedResponse: output.NewTimestamped(),
				Success:             true,
				RepoPath:            repoPath,
				ProjectKey:          projectKey,
				HookPath:            hookPath,
			})
		}

		okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
		mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

		fmt.Println()
		fmt.Printf("  %s Pre-commit guard installed\n", okStyle.Render("✓"))
		fmt.Printf("    Repository:  %s\n", repoPath)
		fmt.Printf("    Project key: %s\n", projectKey)
		fmt.Printf("    Hook:        %s\n", mutedStyle.Render(hookPath))
		fmt.Println()
		return nil
	}
	// Fall through to fallback if MCP fails

	// Fallback: Install basic guard script manually
	if err := installFallbackGuard(hookPath, projectKey, repoPath); err != nil {
		return fmt.Errorf("installing guard: %w", err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(GuardsInstallResponse{
			TimestampedResponse: output.NewTimestamped(),
			Success:             true,
			RepoPath:            repoPath,
			ProjectKey:          projectKey,
			HookPath:            hookPath,
			Message:             "Installed using fallback (Agent Mail MCP not available)",
		})
	}

	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Printf("  %s Pre-commit guard installed\n", okStyle.Render("✓"))
	fmt.Printf("    Repository:  %s\n", repoPath)
	fmt.Printf("    Project key: %s\n", projectKey)
	fmt.Printf("    Hook:        %s\n", mutedStyle.Render(hookPath))
	fmt.Printf("  %s Agent Mail MCP not available - using fallback\n", warnStyle.Render("⚠"))
	fmt.Println()

	return nil
}

func installFallbackGuard(hookPath, projectKey, repoPath string) error {
	// Ensure hooks directory exists
	hookDir := filepath.Dir(hookPath)
	if err := os.MkdirAll(hookDir, 0755); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}

	// Sanitize paths for shell comments (replace newlines and special chars)
	safeProjectKey := sanitizeForShellComment(projectKey)
	safeRepoPath := sanitizeForShellComment(repoPath)

	script := fmt.Sprintf(`#!/bin/bash
# ntm-precommit-guard
# Installed by: ntm guards install (fallback; Agent Mail MCP unavailable at install time)
# Project: %s
# Repository: %s
#
# Real reservation check (bd-ws1-truth-safety-l5ddi.1): delegates to
# 'ntm guards check --staged', which queries Agent Mail for active exclusive
# file reservations overlapping the staged paths and blocks the commit on
# conflict, naming the holder + reservation. If Agent Mail is unreachable the
# check fails OPEN with a visible WARN and a degraded-event row surfaced by
# 'ntm doctor'; set NTM_GUARD_STRICT=1 to fail closed instead.

if ! command -v ntm >/dev/null 2>&1; then
    if [ "${NTM_GUARD_STRICT:-}" = "1" ]; then
        echo "[ntm-guard] BLOCKED: ntm not found on PATH and NTM_GUARD_STRICT=1 (fail-closed)" >&2
        exit 1
    fi
    echo "[ntm-guard] WARN: ntm not found on PATH; skipping reservation check" >&2
    exit 0
fi

exec ntm guards check --staged --project-key %s
`, safeProjectKey, safeRepoPath, shellSingleQuote(projectKey))

	return writeGuardHookFile(hookPath, script)
}

func newGuardsUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove pre-commit guard from current repository",
		RunE:  runGuardsUninstall,
	}
}

// GuardsUninstallResponse is the JSON output for guards uninstall.
type GuardsUninstallResponse struct {
	output.TimestampedResponse
	Success  bool   `json:"success"`
	RepoPath string `json:"repo_path"`
	HookPath string `json:"hook_path"`
	Message  string `json:"message,omitempty"`
}

func runGuardsUninstall(cmd *cobra.Command, args []string) error {
	// Get current directory
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find git root
	repoPath, err := findGitRoot(cwd)
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	hookPath, err := findGitHookPath(repoPath, "pre-commit")
	if err != nil {
		return err
	}

	// Check if hook exists
	if !fileExists(hookPath) {
		if IsJSONOutput() {
			return output.PrintJSON(GuardsUninstallResponse{
				TimestampedResponse: output.NewTimestamped(),
				Success:             true,
				RepoPath:            repoPath,
				HookPath:            hookPath,
				Message:             "No guard installed",
			})
		}

		mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		fmt.Println()
		fmt.Printf("  %s No pre-commit guard installed\n", mutedStyle.Render("•"))
		fmt.Println()
		return nil
	}

	// Check if it's our hook
	content, err := os.ReadFile(hookPath)
	if err != nil {
		return fmt.Errorf("reading hook: %w", err)
	}

	if !strings.Contains(string(content), "ntm-precommit-guard") {
		return fmt.Errorf("pre-commit hook at %s is not an NTM guard - refusing to remove", hookPath)
	}

	// Try using Agent Mail MCP first
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := agentmail.NewClient()
	err = client.UninstallPrecommitGuard(ctx, repoPath)
	if err == nil {
		if IsJSONOutput() {
			return output.PrintJSON(GuardsUninstallResponse{
				TimestampedResponse: output.NewTimestamped(),
				Success:             true,
				RepoPath:            repoPath,
				HookPath:            hookPath,
			})
		}

		okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
		fmt.Println()
		fmt.Printf("  %s Pre-commit guard removed\n", okStyle.Render("✓"))
		fmt.Println()
		return nil
	}
	// Fall through to manual removal

	// Fallback: Remove manually
	if err := os.Remove(hookPath); err != nil {
		return fmt.Errorf("removing hook: %w", err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(GuardsUninstallResponse{
			TimestampedResponse: output.NewTimestamped(),
			Success:             true,
			RepoPath:            repoPath,
			HookPath:            hookPath,
		})
	}

	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	fmt.Println()
	fmt.Printf("  %s Pre-commit guard removed from %s\n", okStyle.Render("✓"), repoPath)
	fmt.Println()

	return nil
}

func newGuardsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show pre-commit guard status",
		RunE:  runGuardsStatus,
	}
}

// GuardsStatusResponse is the JSON output for guards status.
type GuardsStatusResponse struct {
	output.TimestampedResponse
	Installed    bool                       `json:"installed"`
	RepoPath     string                     `json:"repo_path"`
	HookPath     string                     `json:"hook_path"`
	ProjectKey   string                     `json:"project_key,omitempty"`
	IsNTMGuard   bool                       `json:"is_ntm_guard"`
	OtherHook    bool                       `json:"other_hook"`
	MCPAvailable bool                       `json:"mcp_available"`
	DegradedRuns int                        `json:"degraded_runs"`
	LastDegraded []state.GuardDegradedEvent `json:"last_degraded_events,omitempty"`
}

func runGuardsStatus(cmd *cobra.Command, args []string) error {
	// Get current directory
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find git root
	repoPath, err := findGitRoot(cwd)
	if err != nil {
		if IsJSONOutput() {
			return output.PrintJSON(GuardsStatusResponse{
				TimestampedResponse: output.NewTimestamped(),
				Installed:           false,
			})
		}

		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		fmt.Println()
		fmt.Printf("  %s Not in a git repository\n", errorStyle.Render("✗"))
		fmt.Println()
		return nil
	}

	hookPath, err := findGitHookPath(repoPath, "pre-commit")
	if err != nil {
		return err
	}

	// Check MCP availability using the IsAvailable() method
	client := agentmail.NewClient()
	mcpAvailable := client.IsAvailable()

	// Check hook status
	installed := fileExists(hookPath)
	isNTMGuard := false
	otherHook := false
	projectKey := ""

	if installed {
		content, err := os.ReadFile(hookPath)
		if err == nil {
			contentStr := string(content)
			if strings.Contains(contentStr, "ntm-precommit-guard") {
				isNTMGuard = true
				// Try to extract project key
				for _, line := range strings.Split(contentStr, "\n") {
					if strings.HasPrefix(line, "# Project: ") {
						projectKey = strings.TrimPrefix(line, "# Project: ")
						break
					}
				}
			} else {
				otherHook = true
			}
		}
	}

	// Degraded-mode visibility (bd-ws1-truth-safety-l5ddi.1): count of hook
	// runs that failed open because Agent Mail was unreachable.
	degradedRuns := 0
	var lastDegraded []state.GuardDegradedEvent
	if store, storeErr := state.Open(""); storeErr == nil {
		if migrateErr := store.Migrate(); migrateErr == nil {
			if stats, statsErr := store.GuardDegradedEventStats(time.Time{}); statsErr == nil {
				degradedRuns = stats.Count
			}
			if degradedRuns > 0 {
				lastDegraded, _ = store.ListGuardDegradedEvents(3)
			}
		}
		_ = store.Close()
	}

	if IsJSONOutput() {
		return output.PrintJSON(GuardsStatusResponse{
			TimestampedResponse: output.NewTimestamped(),
			Installed:           installed && isNTMGuard,
			RepoPath:            repoPath,
			HookPath:            hookPath,
			ProjectKey:          projectKey,
			IsNTMGuard:          isNTMGuard,
			OtherHook:           otherHook,
			MCPAvailable:        mcpAvailable,
			DegradedRuns:        degradedRuns,
			LastDegraded:        lastDegraded,
		})
	}

	// TUI output
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("NTM Guards Status"))
	fmt.Println()

	fmt.Printf("  Repository: %s\n", repoPath)
	fmt.Println()

	// Guard status
	if !installed {
		fmt.Printf("  %s Pre-commit guard: not installed\n", mutedStyle.Render("○"))
	} else if isNTMGuard {
		fmt.Printf("  %s Pre-commit guard: installed\n", okStyle.Render("✓"))
		if projectKey != "" {
			fmt.Printf("    Project: %s\n", projectKey)
		}
	} else {
		fmt.Printf("  %s Pre-commit hook exists (not NTM guard)\n", warnStyle.Render("⚠"))
	}

	fmt.Printf("    Hook path: %s\n", mutedStyle.Render(hookPath))
	fmt.Println()

	// MCP status
	if mcpAvailable {
		fmt.Printf("  %s Agent Mail MCP: available\n", okStyle.Render("✓"))
	} else {
		fmt.Printf("  %s Agent Mail MCP: not available\n", mutedStyle.Render("○"))
	}

	// Degraded-run visibility
	if degradedRuns > 0 {
		fmt.Println()
		fmt.Printf("  %s Guard hook ran degraded %d time(s) — commits were allowed WITHOUT a reservation check\n", warnStyle.Render("⚠"), degradedRuns)
		for _, ev := range lastDegraded {
			fmt.Printf("    %s %s (%s)\n", mutedStyle.Render(ev.CreatedAt.Local().Format("2006-01-02 15:04:05")), ev.Reason, ev.RepoPath)
		}
	}

	// Installation hint
	if !installed || !isNTMGuard {
		fmt.Println()
		if otherHook {
			fmt.Printf("  %s\n", mutedStyle.Render("Use 'ntm guards install --force' to replace existing hook"))
		} else {
			fmt.Printf("  %s\n", mutedStyle.Render("Run 'ntm guards install' to install the guard"))
		}
	}

	fmt.Println()
	return nil
}

// findGitRoot finds the root of the git repository starting from the given path.
func findGitRoot(startPath string) (string, error) {
	cmd := exec.Command("git", "-C", startPath, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("finding git root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func findGitHookPath(repoPath, hookName string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--git-path", "hooks/"+hookName)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolving git hook path: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("resolving git hook path: empty path")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(repoPath, path), nil
}

func writeGuardHookFile(path, content string) error {
	hookDir := filepath.Dir(path)
	if err := os.MkdirAll(hookDir, 0755); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}
	tmp, err := os.CreateTemp(hookDir, ".ntm-guard-*")
	if err != nil {
		return fmt.Errorf("creating temporary hook: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temporary hook: %w", err)
	}
	if err := tmp.Chmod(0755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temporary hook: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary hook: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("installing hook: %w", err)
	}
	cleanup = false
	return nil
}

// sanitizeForShellComment sanitizes a string for safe inclusion in a shell comment.
// Replaces newlines with spaces and removes control characters that could break script structure.
func sanitizeForShellComment(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}
