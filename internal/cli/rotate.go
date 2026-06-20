package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/auth"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/rotation"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newRotateCmd() *cobra.Command {
	var paneIndex int
	var preserveContext bool
	var targetAccount string
	var dryRun bool
	var timeout int
	var allLimited bool

	cmd := &cobra.Command{
		Use:   "rotate [session]",
		Short: "Rotate to a different account when rate limited",
		Long: `Helps switch AI agent accounts when hitting rate limits.

By default, uses the restart strategy (quit session, switch browser account, start fresh).
Use --preserve-context to re-authenticate the existing session instead.

Examples:
  ntm rotate myproject --pane=0
  ntm rotate myproject --all-limited       # Rotate all rate-limited panes
  ntm rotate myproject --pane=0 --preserve-context
  ntm rotate myproject --pane=0 --account=backup1@gmail.com`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			var session string
			if len(args) > 0 {
				session = args[0]
			}

			res, err := ResolveSession(session, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if res.Session == "" {
				return fmt.Errorf("session required")
			}
			res.ExplainIfInferred(os.Stderr)
			session = res.Session

			if allLimited {
				return rotateAllLimited(session, targetAccount, dryRun, res.Inferred)
			}

			if paneIndex < 0 {
				return fmt.Errorf("pane index required (use --pane=N) or --all-limited")
			}

			// Get pane info
			panes, err := tmux.GetPanes(session)
			if err != nil {
				return fmt.Errorf("getting panes: %w", err)
			}
			var paneID string
			var provider string
			var modelAlias string
			for _, p := range panes {
				if p.Index == paneIndex {
					paneID = p.ID
					provider = normalizedProviderName(p.Type)
					modelAlias = p.Variant
					break
				}
			}
			if paneID == "" {
				return fmt.Errorf("pane %d not found in session %s", paneIndex, session)
			}

			// Suggest account from config if not specified
			if targetAccount == "" && cfg != nil {
				if suggested := cfg.Rotation.SuggestNextAccount(provider, ""); suggested != nil {
					targetAccount = suggested.Email
					if suggested.Alias != "" {
						targetAccount = fmt.Sprintf("%s (%s)", suggested.Email, suggested.Alias)
					}
				}
			}
			if targetAccount == "" {
				targetAccount = "<your other account>"
			}

			if dryRun {
				strategy := "restart"
				if preserveContext {
					strategy = "re-auth"
				}
				fmt.Printf("Dry run: rotate session=%s pane=%d provider=%s model=%s strategy=%s account=%s\n",
					session, paneIndex, provider, modelAlias, strategy, targetAccount)
				return nil
			}

			if preserveContext {
				return executeReauthRotation(session, paneIndex, paneID, provider, time.Duration(timeout)*time.Second)
			}
			return executeRestartRotation(session, paneIndex, paneID, provider, targetAccount, modelAlias, res.Inferred)
		},
	}

	cmd.Flags().IntVar(&paneIndex, "pane", -1, "Pane index to rotate")
	cmd.Flags().BoolVar(&preserveContext, "preserve-context", false, "Re-authenticate existing session instead of restarting")
	cmd.Flags().StringVar(&targetAccount, "account", "", "Target account email (optional)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print action without executing")
	cmd.Flags().IntVar(&timeout, "timeout", 300, "Timeout in seconds for auth completion")
	cmd.Flags().BoolVar(&allLimited, "all-limited", false, "Rotate all rate-limited panes in the session")
	cmd.ValidArgsFunction = completeSessionArgs
	_ = cmd.RegisterFlagCompletionFunc("pane", completePaneIndexes)

	// Add context rotation management subcommand
	cmd.AddCommand(newRotateContextCmd())

	// Add account pin (lock/unlock/status) management subcommands. These control
	// whether automatic account rotation (the caam-switch path triggered on a
	// usage-limit hit) is allowed to rotate away from an operator-pinned account.
	cmd.AddCommand(newRotateLockCmd())
	cmd.AddCommand(newRotateUnlockCmd())
	cmd.AddCommand(newRotateStatusCmd())

	return cmd
}

// rotatePinDataDir resolves the directory whose .ntm/account_pins.json holds the
// shared account pins. Honors an explicit override, else the current directory.
func rotatePinDataDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return os.Getwd()
}

func newRotateLockCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "lock <provider> <account>",
		Short: "Pin a provider to an account so auto-rotation won't switch away from it",
		Long: `Pin (lock) an account for a provider so the automatic account rotator refuses
to rotate away from it on a usage-limit hit. The pin is persisted to
<data-dir>/.ntm/account_pins.json and is honored by any running swarm.

provider may be an agent type (cc, cod, gmi) or a caam provider (claude, openai, google).

Example:
  ntm rotate lock cod acctB    # never auto-rotate Codex away from acctB`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := rotatePinDataDir(dataDir)
			if err != nil {
				return err
			}
			rotator := swarm.NewAccountRotator()
			if err := rotator.LoadPins(dir); err != nil {
				return err
			}
			rotator.PinAccount(args[0], args[1])
			if err := rotator.SavePins(dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pinned %s to %q; automatic rotation will refuse to switch away (use --force-global-auth-clobber or 'ntm rotate unlock' to override).\n", args[0], args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Directory holding .ntm/account_pins.json (default: current directory)")
	return cmd
}

func newRotateUnlockCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "unlock <provider>",
		Short: "Remove an account pin, re-enabling automatic rotation for the provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := rotatePinDataDir(dataDir)
			if err != nil {
				return err
			}
			rotator := swarm.NewAccountRotator()
			if err := rotator.LoadPins(dir); err != nil {
				return err
			}
			rotator.UnpinAccount(args[0])
			if err := rotator.SavePins(dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Unpinned %s; automatic rotation re-enabled.\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Directory holding .ntm/account_pins.json (default: current directory)")
	return cmd
}

func newRotateStatusCmd() *cobra.Command {
	var dataDir string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show account pins and caam safe-restore capability that gate automatic rotation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := rotatePinDataDir(dataDir)
			if err != nil {
				return err
			}
			rotator := swarm.NewAccountRotator()
			if err := rotator.LoadPins(dir); err != nil {
				return err
			}
			pins := rotator.PinnedAccounts()

			// Probe caam for the safe-restore capability (caam #19) so operators can
			// see whether a global Codex rotation would be permitted.
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			safeRestore := false
			capErr := ""
			if rotator.IsAvailable() {
				ok, probeErr := rotator.CaamSupportsSafeRestore(ctx)
				if probeErr != nil {
					capErr = probeErr.Error()
				} else {
					safeRestore = ok
				}
			} else {
				capErr = "caam not available"
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				out := map[string]interface{}{
					"pins":                  pins,
					"caam_safe_restore":     safeRestore,
					"global_rotation_safe":  safeRestore,
					"safe_restore_required": true,
				}
				if capErr != "" {
					out["caam_capability_error"] = capErr
				}
				return enc.Encode(out)
			}
			if len(pins) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No account pins set; automatic rotation is unrestricted by pins.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Pinned providers (auto-rotation refused for these):")
				for provider, account := range pins {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s -> %s\n", provider, account)
				}
			}
			switch {
			case capErr != "":
				fmt.Fprintf(cmd.OutOrStdout(), "caam safe-restore: UNKNOWN (%s) — global Codex rotation refused without --force-global-auth-clobber.\n", capErr)
			case safeRestore:
				fmt.Fprintln(cmd.OutOrStdout(), "caam safe-restore: AVAILABLE — global Codex rotation permitted (caam #19 satisfied).")
			default:
				fmt.Fprintln(cmd.OutOrStdout(), "caam safe-restore: MISSING — global Codex rotation refused; upgrade caam (#19) or use --force-global-auth-clobber.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Directory holding .ntm/account_pins.json (default: current directory)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func rotateAllLimited(session, targetAccount string, dryRun bool, inferred bool) error {
	// 1. Identify limited panes
	fmt.Printf("Scanning session '%s' for rate-limited panes...\n", session)
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return err
	}

	var limitedPanes []tmux.Pane
	fetcher := &quota.PTYFetcher{CommandTimeout: 5 * time.Second}
	ctx := context.Background()

	for _, p := range panes {
		// Skip user panes
		if tmux.AgentType(p.Type).Canonical() == tmux.AgentUser {
			continue
		}

		// Check quota
		provider, ok := quotaProviderForAgentType(p.Type)
		if !ok {
			continue
		}

		fmt.Printf("  Checking %s (index %d)... ", p.Title, p.Index)
		info, err := fetcher.FetchQuota(ctx, p.ID, provider)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		if info.IsLimited {
			fmt.Printf("LIMITED\n")
			limitedPanes = append(limitedPanes, p)
		} else {
			fmt.Printf("OK\n")
		}
	}

	if len(limitedPanes) == 0 {
		fmt.Println("No rate-limited panes found.")
		return nil
	}

	fmt.Printf("\nFound %d limited panes to rotate.\n", len(limitedPanes))

	// Suggest account from config if not specified
	if targetAccount == "" && cfg != nil {
		// Use first limited pane to suggest account
		providerStr := normalizedProviderName(limitedPanes[0].Type)
		if suggested := cfg.Rotation.SuggestNextAccount(providerStr, ""); suggested != nil {
			targetAccount = suggested.Email
			if suggested.Alias != "" {
				targetAccount = fmt.Sprintf("%s (%s)", suggested.Email, suggested.Alias)
			}
		}
	}
	if targetAccount == "" {
		targetAccount = "<your other account>"
	}

	if dryRun {
		fmt.Printf("Dry run: would rotate %d panes to %s\n", len(limitedPanes), targetAccount)
		return nil
	}

	// Batch Rotation Flow
	orchestrator := auth.NewOrchestrator(cfg)
	projectDir, err := resolveRotationProjectDir(session, inferred)
	if err != nil {
		return err
	}

	// 1. Terminate all
	fmt.Println("\nStep 1/3: Terminating sessions...")
	for _, p := range limitedPanes {
		fmt.Printf("  Terminating %s (pane %d)...\n", p.Title, p.Index)
		if err := orchestrator.TerminateSession(p.ID, normalizedProviderName(p.Type)); err != nil {
			fmt.Printf("    Error terminating: %v\n", err)
		}
	}

	// 2. Wait for shells
	fmt.Println("Step 2/3: Waiting for shell prompts...")
	for _, p := range limitedPanes {
		_ = orchestrator.WaitForShellPrompt(p.ID, 5*time.Second)
	}

	// 3. Prompt user ONCE
	fmt.Printf("\n")
	fmt.Printf("╔════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  👉 ACTION REQUIRED                                    ║\n")
	fmt.Printf("╠════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Switch your browser to:                               ║\n")
	fmt.Printf("║    %s\n", targetAccount)
	fmt.Printf("║                                                        ║\n")
	fmt.Printf("║  This will authenticate %d agents at once.              ║\n", len(limitedPanes))
	fmt.Printf("║  Then press ENTER to continue...                       ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════╝\n")

	if cfg != nil && cfg.Rotation.AutoOpenBrowser {
		openAccountsPage()
	}
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')

	// 4. Start all
	fmt.Println("\nStep 3/3: Starting new sessions...")
	for _, p := range limitedPanes {
		fmt.Printf("  Starting %s...\n", p.Title)
		ctx := auth.RestartContext{
			PaneID:      p.ID,
			Provider:    normalizedProviderName(p.Type),
			TargetEmail: targetAccount,
			ModelAlias:  p.Variant,
			SessionName: session,
			PaneIndex:   p.Index,
			ProjectDir:  projectDir,
		}
		if err := orchestrator.StartNewAgentSession(ctx); err != nil {
			fmt.Printf("    Error starting: %v\n", err)
		}
	}

	fmt.Println("\n✓ Batch rotation complete.")
	return nil
}

func resolveRotationProjectDir(session string, inferred bool) (string, error) {
	projectDir := resolveCommandProjectDirForSession(session, inferred)
	if projectDir == "" {
		return "", fmt.Errorf("getting project root failed")
	}
	return projectDir, nil
}

func executeRestartRotation(session string, paneIdx int, paneID, provider, targetAccount, modelAlias string, inferred bool) error {
	// Initialize Orchestrator
	orchestrator := auth.NewOrchestrator(cfg)
	projectDir, err := resolveRotationProjectDir(session, inferred)
	if err != nil {
		return err
	}

	fmt.Printf("╔════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  ACCOUNT ROTATION - Restart Strategy                   ║\n")
	fmt.Printf("╠════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Session: %-44s ║\n", session)
	fmt.Printf("║  Pane:    %-44d ║\n", paneIdx)
	fmt.Printf("║  Provider: %-43s ║\n", provider)
	fmt.Printf("╚════════════════════════════════════════════════════════╝\n\n")

	ctx := auth.RestartContext{
		PaneID:      paneID,
		Provider:    provider,
		TargetEmail: targetAccount,
		ModelAlias:  modelAlias,
		SessionName: session,
		PaneIndex:   paneIdx,
		ProjectDir:  projectDir,
	}

	if err := orchestrator.ExecuteRestartStrategy(ctx); err != nil {
		return err
	}

	fmt.Println("\n✓ Rotation complete! New session started.")
	fmt.Println("  The new session will use your currently active browser account.")

	return nil
}

func executeReauthRotation(session string, paneIdx int, paneID, provider string, timeout time.Duration) error {
	fmt.Printf("╔════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  ACCOUNT ROTATION - Re-auth Strategy                   ║\n")
	fmt.Printf("╠════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Session: %-44s ║\n", session)
	fmt.Printf("║  Pane:    %-44d ║\n", paneIdx)
	fmt.Printf("║  Provider: %-43s ║\n", provider)
	fmt.Printf("╚════════════════════════════════════════════════════════╝\n\n")

	prov := rotation.GetProvider(provider)
	if prov == nil {
		return fmt.Errorf("unknown provider: %s", provider)
	}

	if !prov.SupportsReauth() {
		return fmt.Errorf("re-auth strategy not supported for provider %s (try restart strategy)", prov.Name())
	}

	// Step 1: Send login command
	fmt.Printf("Step 1/3: Sending %s command...\n", prov.LoginCommand())

	// Only Claude has specialized auth flow implementation for now
	// For others, we might need generic flow or specific implementations
	if prov.Name() != "Claude" {
		return fmt.Errorf("re-auth flow implementation pending for %s", prov.Name())
	}

	authFlow := auth.NewClaudeAuthFlow(false) // false = not remote/SSH
	if err := authFlow.InitiateAuth(paneID); err != nil {
		return fmt.Errorf("initiating auth: %w", err)
	}
	fmt.Println("  ✓ Login command sent")

	// Step 2: Wait for auth completion
	fmt.Println("\nStep 2/3: Waiting for authentication...")
	fmt.Println("  Complete the browser authentication...")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := authFlow.MonitorAuth(ctx, paneID)
	if err != nil {
		return fmt.Errorf("monitoring auth: %w", err)
	}

	switch result.State {
	case auth.AuthSuccess:
		fmt.Println("  ✓ Authentication successful!")
	case auth.AuthNeedsBrowser:
		fmt.Printf("\n  Browser auth URL: %s\n", result.URL)
		fmt.Println("  Complete the authentication in your browser...")
		// Continue monitoring
		result, err = authFlow.MonitorAuth(ctx, paneID)
		if err != nil || result.State != auth.AuthSuccess {
			return fmt.Errorf("authentication failed or timed out")
		}
		fmt.Println("  ✓ Authentication successful!")
	case auth.AuthNeedsChallenge:
		fmt.Println("  Challenge code required (SSH/remote mode)")
		fmt.Println("  Enter the code displayed in the browser into the agent pane")
		// Continue monitoring
		result, err = authFlow.MonitorAuth(ctx, paneID)
		if err != nil || result.State != auth.AuthSuccess {
			return fmt.Errorf("authentication failed or timed out")
		}
		fmt.Println("  ✓ Authentication successful!")
	case auth.AuthFailed:
		return fmt.Errorf("authentication failed: %v", result.Error)
	}

	// Step 3: Send continuation prompt
	fmt.Println("\nStep 3/3: Sending continuation prompt...")
	continuation := prov.ContinuationPrompt()
	if cfg != nil && cfg.Rotation.ContinuationPrompt != "" {
		continuation = cfg.Rotation.ContinuationPrompt
	}
	if err := authFlow.SendContinuation(paneID, continuation); err != nil {
		return fmt.Errorf("sending continuation: %w", err)
	}
	fmt.Println("  ✓ Continuation sent")

	fmt.Println("\n✓ Re-auth complete! Session context preserved.")

	return nil
}

// openAccountsPage opens the Google accounts page in the default browser
func openAccountsPage() {
	// Use 'open' on macOS, 'xdg-open' on Linux
	// For now, just print the URL
	fmt.Println("  Tip: Visit https://accounts.google.com to switch accounts")
}
