package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/auth"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/spf13/cobra"
)

func newRotateCmd() *cobra.Command {
	var paneIndex int
	var preserveContext bool
	var targetAccount string
	var dryRun bool
	var timeout int

	cmd := &cobra.Command{
		Use:   "rotate [session]",
		Short: "Rotate to a different account when rate limited",
		Long: `Helps switch AI agent accounts when hitting rate limits.

By default, uses the restart strategy (quit session, switch browser account, start fresh).
Use --preserve-context to re-authenticate the existing session instead.

Examples:
  ntm rotate myproject --pane=0
  ntm rotate myproject --pane=0 --preserve-context
  ntm rotate myproject --pane=0 --account=backup1@gmail.com`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			} else {
				if !tmux.InTmux() {
					return fmt.Errorf("session name required when not in tmux")
				}
				session = tmux.GetCurrentSession()
			}

			if paneIndex < 0 {
				return fmt.Errorf("pane index required (use --pane=N)")
			}

			// Get pane info
			panes, err := tmux.GetPanes(session)
			if err != nil {
				return fmt.Errorf("getting panes: %w", err)
			}
			var paneID string
			var provider string
			for _, p := range panes {
				if p.Index == paneIndex {
					paneID = p.ID
					provider = string(p.Type)
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
				fmt.Printf("Dry run: rotate session=%s pane=%d provider=%s strategy=%s account=%s\n",
					session, paneIndex, provider, strategy, targetAccount)
				return nil
			}

			if preserveContext {
				return executeReauthRotation(session, paneIndex, paneID, provider, time.Duration(timeout)*time.Second)
			}
			return executeRestartRotation(session, paneIndex, paneID, provider, targetAccount)
		},
	}

	cmd.Flags().IntVar(&paneIndex, "pane", -1, "Pane index to rotate")
	cmd.Flags().BoolVar(&preserveContext, "preserve-context", false, "Re-authenticate existing session instead of restarting")
	cmd.Flags().StringVar(&targetAccount, "account", "", "Target account email (optional)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print action without executing")
	cmd.Flags().IntVar(&timeout, "timeout", 300, "Timeout in seconds for auth completion")

	return cmd
}

func executeRestartRotation(session string, paneIdx int, paneID, provider, targetAccount string) error {
	// Initialize Orchestrator
	orchestrator := auth.NewOrchestrator(cfg)

	fmt.Printf("╔════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  ACCOUNT ROTATION - Restart Strategy                   ║\n")
	fmt.Printf("╠════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Session: %-44s ║\n", session)
	fmt.Printf("║  Pane:    %-44d ║\n", paneIdx)
	fmt.Printf("║  Provider: %-43s ║\n", provider)
	fmt.Printf("╚════════════════════════════════════════════════════════╝\n\n")

	// Step 1: Terminate session
	fmt.Println("Step 1/3: Terminating agent session...")
	if err := orchestrator.TerminateSession(paneID); err != nil {
		return fmt.Errorf("terminating session: %w", err)
	}

	// Step 2: Wait for shell prompt
	fmt.Println("Step 2/3: Waiting for shell prompt...")
	if err := orchestrator.WaitForShellPrompt(paneID, 10*time.Second); err != nil {
		return fmt.Errorf("session did not terminate cleanly: %w", err)
	}
	fmt.Println("  ✓ Session terminated")

	// Step 3: Prompt user for browser auth
	fmt.Printf("\n")
	fmt.Printf("╔════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  👉 ACTION REQUIRED                                    ║\n")
	fmt.Printf("╠════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Switch your browser to:                               ║\n")
	fmt.Printf("║    %s\n", targetAccount)
	fmt.Printf("║                                                        ║\n")
	fmt.Printf("║  Then press ENTER to continue...                       ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════╝\n")

	// Open browser to Google accounts if configured
	if cfg != nil && cfg.Rotation.AutoOpenBrowser {
		openAccountsPage()
	}

	// Wait for user confirmation
	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadString('\n')

	// Step 4: Start new agent session
	fmt.Println("\nStep 3/3: Starting new agent session...")
	if err := orchestrator.StartNewAgentSession(paneID, provider); err != nil {
		return fmt.Errorf("starting new session: %w", err)
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

	// Only Claude supported for re-auth currently
	if provider != "cc" && provider != "claude" {
		return fmt.Errorf("re-auth strategy only supported for Claude currently (provider=%s)", provider)
	}

	// Step 1: Send /login command
	fmt.Println("Step 1/3: Sending /login command...")
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
	continuation := "continue. Use ultrathink"
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
