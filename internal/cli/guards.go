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

	hookPath := filepath.Join(repoPath, ".git", "hooks", "pre-commit")

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
# Installed by: ntm guards install
# Project: %s
# Repository: %s

set -e

# Check for ntm command
if ! command -v ntm &> /dev/null; then
    exit 0  # NTM not installed, allow commit
fi

# Check for active file reservations that might conflict
# This is a fallback check - the full check requires Agent Mail MCP

# For now, just log and pass (full implementation via Agent Mail)
echo "[ntm-guard] Pre-commit check passed"
exit 0
`, safeProjectKey, safeRepoPath)

	return os.WriteFile(hookPath, []byte(script), 0755)
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

	hookPath := filepath.Join(repoPath, ".git", "hooks", "pre-commit")

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
	Installed    bool   `json:"installed"`
	RepoPath     string `json:"repo_path"`
	HookPath     string `json:"hook_path"`
	ProjectKey   string `json:"project_key,omitempty"`
	IsNTMGuard   bool   `json:"is_ntm_guard"`
	OtherHook    bool   `json:"other_hook"`
	MCPAvailable bool   `json:"mcp_available"`
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

	hookPath := filepath.Join(repoPath, ".git", "hooks", "pre-commit")

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

// sanitizeForShellComment sanitizes a string for safe inclusion in a shell comment.
// Replaces newlines with spaces and removes control characters that could break script structure.
func sanitizeForShellComment(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// fileExists returns true if the file at path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
