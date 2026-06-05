package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Manage git hooks for quality checks and coordination",
		Long: `Install and manage git hooks for quality checks (UBS) and coordination (Agent Mail).

UBS pre-commit hook:
- Runs UBS on staged files only (fast path)
- Blocks commits if critical or warning issues are found
- Provides clear, actionable error messages

Agent Mail pre-commit guard:
- Blocks commits when files are reserved by other agents
- Optional warn-only mode via AGENT_MAIL_GUARD_MODE=warn

Examples:
  ntm hooks install                 # Install UBS pre-commit hook
  ntm hooks install --force         # Overwrite existing UBS hook
  ntm hooks status                  # Check UBS hook status
  ntm hooks uninstall               # Remove UBS hook
  ntm hooks run pre-commit          # Run UBS hook manually

  ntm hooks guard install           # Install Agent Mail pre-commit guard
  ntm hooks guard install --warn-only  # Print warn-only setup instructions
  ntm hooks guard uninstall          # Remove Agent Mail pre-commit guard`,
	}

	cmd.AddCommand(
		newHooksInstallCmd(),
		newHooksUninstallCmd(),
		newHooksStatusCmd(),
		newHooksRunCmd(),
		newHooksGuardCmd(),
	)

	return cmd
}

func newHooksInstallCmd() *cobra.Command {
	var (
		force    bool
		hookType string
	)

	cmd := &cobra.Command{
		Use:   "install [hook-type]",
		Short: "Install a git hook",
		Long: `Install a git hook. Currently supports:
  - pre-commit: Syncs beads (if available) and runs UBS on staged files
  - post-checkout: Warns when .beads has uncommitted changes

If no hook type is specified, installs pre-commit.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				hookType = args[0]
			} else {
				hookType = "pre-commit"
			}
			return runHooksInstall(hookType, force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing hook")

	return cmd
}

func runHooksInstall(hookType string, force bool) error {
	t := theme.Current()

	mgr, err := hooks.NewManager("")
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
		}
		return err
	}

	ht := hooks.HookType(hookType)

	if err := mgr.Install(ht, force); err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"success":   false,
				"error":     err.Error(),
				"hook_type": hookType,
			})
		}
		if err == hooks.ErrHookExists {
			fmt.Printf("%s✗%s Hook already exists. Use --force to overwrite.\n",
				colorize(t.Error), "\033[0m")
			return nil
		}
		return err
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"success":   true,
			"hook_type": hookType,
			"path":      mgr.HooksDir() + "/" + hookType,
		})
	}

	fmt.Printf("%s✓%s Installed %s hook\n", colorize(t.Success), "\033[0m", hookType)
	fmt.Printf("  Location: %s/%s\n", mgr.HooksDir(), hookType)
	return nil
}

func newHooksUninstallCmd() *cobra.Command {
	var (
		restore  bool
		hookType string
	)

	cmd := &cobra.Command{
		Use:   "uninstall [hook-type]",
		Short: "Remove a git hook",
		Long:  `Remove an NTM-managed git hook. Optionally restore the backup if one exists.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				hookType = args[0]
			} else {
				hookType = "pre-commit"
			}
			return runHooksUninstall(hookType, restore)
		},
	}

	cmd.Flags().BoolVar(&restore, "restore", true, "Restore backup if it exists")

	return cmd
}

func runHooksUninstall(hookType string, restore bool) error {
	t := theme.Current()

	mgr, err := hooks.NewManager("")
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
		}
		return err
	}

	ht := hooks.HookType(hookType)

	if err := mgr.Uninstall(ht, restore); err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"success":   false,
				"error":     err.Error(),
				"hook_type": hookType,
			})
		}
		if err == hooks.ErrHookNotInstalled {
			fmt.Printf("%s•%s Hook not installed\n", "\033[2m", "\033[0m")
			return nil
		}
		return err
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"success":   true,
			"hook_type": hookType,
			"restored":  restore,
		})
	}

	fmt.Printf("%s✓%s Uninstalled %s hook\n", colorize(t.Success), "\033[0m", hookType)
	if restore {
		fmt.Println("  Backup restored if it existed")
	}
	return nil
}

func newHooksStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show status of installed hooks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHooksStatus()
		},
	}
}

func runHooksStatus() error {
	t := theme.Current()

	mgr, err := hooks.NewManager("")
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"error": err.Error(),
			})
		}
		return err
	}

	infos, err := mgr.ListAll()
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"error": err.Error(),
			})
		}
		return err
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"repo_root": mgr.RepoRoot(),
			"hooks_dir": mgr.HooksDir(),
			"hooks":     infos,
		})
	}

	fmt.Println()
	fmt.Printf("\033[1mGit Hooks Status\033[0m\n")
	fmt.Printf("\033[2m════════════════════════════════════════\033[0m\n\n")
	fmt.Printf("  Repository: %s\n", mgr.RepoRoot())
	fmt.Printf("  Hooks dir:  %s\n\n", mgr.HooksDir())

	for _, info := range infos {
		var status, statusColor string
		if info.Installed {
			if info.IsNTM {
				status = "installed (ntm)"
				statusColor = colorize(t.Success)
			} else {
				status = "installed (other)"
				statusColor = colorize(t.Warning)
			}
		} else {
			status = "not installed"
			statusColor = "\033[2m"
		}

		backup := ""
		if info.HasBackup {
			backup = " (backup exists)"
		}

		fmt.Printf("  %-12s %s%s\033[0m%s\n", info.Type, statusColor, status, backup)
	}

	fmt.Println()
	return nil
}

func newHooksRunCmd() *cobra.Command {
	var (
		verbose       bool
		failOnWarning bool
		timeout       int
	)

	cmd := &cobra.Command{
		Use:   "run <hook-type>",
		Short: "Run a hook manually",
		Long: `Run a hook manually without committing. Useful for testing.

This is also called by the installed hook script.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHooksRun(args[0], verbose, failOnWarning, timeout)
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")
	cmd.Flags().BoolVar(&failOnWarning, "fail-on-warning", true, "Fail on warnings")
	cmd.Flags().IntVar(&timeout, "timeout", 60, "Timeout in seconds")

	return cmd
}

func runHooksRun(hookType string, verbose, failOnWarning bool, timeout int) error {
	switch hookType {
	case "pre-commit":
		return runPreCommitHook(verbose, failOnWarning, timeout)
	default:
		return fmt.Errorf("unknown hook type: %s", hookType)
	}
}

func runPreCommitHook(verbose, failOnWarning bool, timeout int) error {
	ctx := context.Background()

	config := hooks.DefaultPreCommitConfig()
	config.Verbose = verbose
	config.FailOnWarning = failOnWarning
	if timeout <= 0 {
		config.Timeout = 0
	} else {
		config.Timeout = time.Duration(timeout) * time.Second
	}

	// Get repo root
	mgr, err := hooks.NewManager("")
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"passed": false,
				"error":  err.Error(),
			})
		}
		return err
	}

	result, err := hooks.RunPreCommit(ctx, mgr.RepoRoot(), config)
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"passed": false,
				"error":  err.Error(),
			})
		}
		return err
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	hooks.PrintPreCommitResult(result)
	result.Exit()
	return nil
}

func newHooksGuardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guard",
		Short: "Manage Agent Mail pre-commit guard",
	}

	cmd.AddCommand(
		newHooksGuardInstallCmd(),
		newHooksGuardUninstallCmd(),
	)

	return cmd
}

func newHooksGuardInstallCmd() *cobra.Command {
	var warnOnly bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Agent Mail pre-commit guard",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHooksGuardInstall(warnOnly)
		},
	}

	cmd.Flags().BoolVar(&warnOnly, "warn-only", false, "Print instructions for warn-only mode (set AGENT_MAIL_GUARD_MODE=warn)")
	return cmd
}

func runHooksGuardInstall(warnOnly bool) error {
	t := theme.Current()
	repoPath, err := os.Getwd()
	if err != nil {
		return err
	}

	client := newAgentMailClient(repoPath)
	if !client.IsAvailable() {
		return fmt.Errorf("agent mail server not available at %s\nstart the server with: am serve-http --host 127.0.0.1 --no-tui --no-auth", agentmail.DefaultBaseURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := client.EnsureProject(ctx, repoPath); err != nil {
		return fmt.Errorf("ensuring project: %w", err)
	}

	if err := client.InstallPrecommitGuard(ctx, repoPath, repoPath); err != nil {
		return fmt.Errorf("installing guard: %w", err)
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"success":   true,
			"warn_only": warnOnly,
			"repo":      repoPath,
		})
	}

	fmt.Printf("%s✓%s Installed Agent Mail pre-commit guard\n", colorize(t.Success), "\033[0m")
	fmt.Printf("  Repo: %s\n", repoPath)
	if warnOnly {
		fmt.Println("  Warn-only: export AGENT_MAIL_GUARD_MODE=warn before committing.")
	} else {
		fmt.Println("  To use warn-only mode, export AGENT_MAIL_GUARD_MODE=warn.")
	}
	return nil
}

func newHooksGuardUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Agent Mail pre-commit guard",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHooksGuardUninstall()
		},
	}
}

func runHooksGuardUninstall() error {
	t := theme.Current()
	repoPath, err := os.Getwd()
	if err != nil {
		return err
	}

	client := newAgentMailClient(repoPath)
	if !client.IsAvailable() {
		return fmt.Errorf("agent mail server not available at %s\nstart the server with: am serve-http --host 127.0.0.1 --no-tui --no-auth", agentmail.DefaultBaseURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := client.UninstallPrecommitGuard(ctx, repoPath); err != nil {
		return fmt.Errorf("uninstalling guard: %w", err)
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"success": true,
			"repo":    repoPath,
		})
	}

	fmt.Printf("%s✓%s Removed Agent Mail pre-commit guard\n", colorize(t.Success), "\033[0m")
	fmt.Printf("  Repo: %s\n", repoPath)
	return nil
}
