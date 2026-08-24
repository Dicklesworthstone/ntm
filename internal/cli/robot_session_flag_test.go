package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

// newRobotSessionTestCmd builds a throwaway command with the same session
// flag wiring as the real root command: the deprecated prefixed flags plus
// the shared --session flag they are deprecated in favor of.
func newRobotSessionTestCmd(t *testing.T) *cobra.Command {
	t.Helper()

	// Save and reset the package globals the flags bind to so parallel
	// state from other tests can't leak in.
	origPipeline := robotPipelineSession
	origTokens := robotTokensSession
	origAlerts := robotAlertsSession
	origPalette := robotPaletteSession
	origShared := robotSharedSession
	t.Cleanup(func() {
		robotPipelineSession = origPipeline
		robotTokensSession = origTokens
		robotAlertsSession = origAlerts
		robotPaletteSession = origPalette
		robotSharedSession = origShared
	})
	robotPipelineSession = ""
	robotTokensSession = ""
	robotAlertsSession = ""
	robotPaletteSession = ""
	robotSharedSession = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().StringVar(&robotPipelineSession, "pipeline-session", "", "")
	cmd.Flags().StringVar(&robotTokensSession, "tokens-session", "", "")
	cmd.Flags().StringVar(&robotAlertsSession, "alerts-session", "", "")
	cmd.Flags().StringVar(&robotPaletteSession, "palette-session", "", "")
	cmd.Flags().StringVar(&robotSharedSession, "session", "", "")
	return cmd
}

// TestResolveRobotSessionSharedFlag is the ntm#214 regression guard: the
// deprecation warnings for --pipeline-session/--tokens-session/
// --alerts-session/--palette-session all say "use --session instead", so
// --session must be registered and actually resolve for each surface.
// Previously the hint pointed at a flag that didn't exist and applying it
// produced `Error: unknown flag: --session`.
func TestResolveRobotSessionSharedFlag(t *testing.T) {
	t.Run("shared --session resolves for all surfaces", func(t *testing.T) {
		cmd := newRobotSessionTestCmd(t)
		if err := cmd.ParseFlags([]string{"--session=proj"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		if got := resolveRobotPipelineSession(cmd); got != "proj" {
			t.Errorf("resolveRobotPipelineSession() = %q, want %q", got, "proj")
		}
		if got := resolveRobotTokensSession(cmd); got != "proj" {
			t.Errorf("resolveRobotTokensSession() = %q, want %q", got, "proj")
		}
		if got := resolveRobotAlertsSession(cmd); got != "proj" {
			t.Errorf("resolveRobotAlertsSession() = %q, want %q", got, "proj")
		}
		if got := resolveRobotPaletteSession(cmd); got != "proj" {
			t.Errorf("resolveRobotPaletteSession() = %q, want %q", got, "proj")
		}
	})

	t.Run("deprecated prefixed flag still works", func(t *testing.T) {
		cmd := newRobotSessionTestCmd(t)
		if err := cmd.ParseFlags([]string{"--pipeline-session=legacy"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		if got := resolveRobotPipelineSession(cmd); got != "legacy" {
			t.Errorf("resolveRobotPipelineSession() = %q, want %q", got, "legacy")
		}
	})

	t.Run("prefixed flag wins when both set", func(t *testing.T) {
		cmd := newRobotSessionTestCmd(t)
		if err := cmd.ParseFlags([]string{"--pipeline-session=legacy", "--session=proj"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		if got := resolveRobotPipelineSession(cmd); got != "legacy" {
			t.Errorf("resolveRobotPipelineSession() = %q, want %q (specific flag takes precedence)", got, "legacy")
		}
	})

	t.Run("neither set resolves empty", func(t *testing.T) {
		cmd := newRobotSessionTestCmd(t)
		if err := cmd.ParseFlags(nil); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		if got := resolveRobotPipelineSession(cmd); got != "" {
			t.Errorf("resolveRobotPipelineSession() = %q, want empty", got)
		}
	})
}

// TestResolveRobotSnapshotSession guards the --robot-snapshot session scope:
// it resolves the shared --session flag through the same helper path as the
// four queries above, with no snapshot-specific prefixed flag to fall back to.
func TestResolveRobotSnapshotSession(t *testing.T) {
	t.Run("shared --session resolves for snapshot", func(t *testing.T) {
		cmd := newRobotSessionTestCmd(t)
		if err := cmd.ParseFlags([]string{"--session=proj"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		if got := resolveRobotSnapshotSession(cmd); got != "proj" {
			t.Errorf("resolveRobotSnapshotSession() = %q, want %q", got, "proj")
		}
	})

	t.Run("no --session resolves empty", func(t *testing.T) {
		cmd := newRobotSessionTestCmd(t)
		if err := cmd.ParseFlags(nil); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		if got := resolveRobotSnapshotSession(cmd); got != "" {
			t.Errorf("resolveRobotSnapshotSession() = %q, want empty", got)
		}
	})
}
