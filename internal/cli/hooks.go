package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Manage git hooks for quality checks",
		Long: `Install and manage git hooks that run UBS (Ultimate Bug Scanner)
before commits to catch bugs early.

The pre-commit hook:
- Runs UBS on staged files only (fast path)
- Blocks commits if critical or warning issues are found
- Provides clear, actionable error messages

Examples:
  ntm hooks install              # Install pre-commit hook
  ntm hooks install --force      # Overwrite existing hook
  ntm hooks status               # Check hook status
  ntm hooks uninstall            # Remove hook
  ntm hooks run pre-commit       # Run hook manually`,
	}

	cmd.AddCommand(
		newHooksInstallCmd(),
		newHooksUninstallCmd(),
		newHooksStatusCmd(),
		newHooksRunCmd(),
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
  - pre-commit: Runs UBS on staged files before commit

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
	config.Timeout = config.Timeout * 1 // Use default

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
