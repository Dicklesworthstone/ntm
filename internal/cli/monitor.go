package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newMonitorCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "internal-monitor <session>",
		Short:  "Run the resilience monitor for a session (internal use)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMonitor(args[0])
		},
	}
}

func runMonitor(session string) error {
	// Load manifest
	manifest, err := resilience.LoadManifest(session)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}

	// Ensure session exists
	if !tmux.SessionExists(session) {
		// If session is gone, clean up and exit
		_ = resilience.DeleteManifest(session)
		return nil
	}

	// Initialize monitor
	monitor := resilience.NewMonitor(session, manifest.ProjectDir, cfg)

	// Register agents
	for _, agent := range manifest.Agents {
		monitor.RegisterAgent(agent.PaneID, agent.PaneIndex, agent.Type, agent.Model, agent.Command)
	}

	// Start monitoring
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	monitor.Start(ctx)

	// Wait for termination signal or session end
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Poll for session existence periodically to exit if session is killed
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	fmt.Printf("Monitoring session '%s' for resilience...\n", session)

	for {
		select {
		case <-sigChan:
			fmt.Println("Monitor stopping...")
			monitor.Stop()
			return nil
		case <-ticker.C:
			if !tmux.SessionExists(session) {
				fmt.Println("Session ended, stopping monitor...")
				monitor.Stop()
				_ = resilience.DeleteManifest(session)
				return nil
			}
		}
	}
}
