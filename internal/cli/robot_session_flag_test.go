package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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

// TestResolveRobotSessionConvergedQueries guards the bucket-C convergence:
// the six queries that used to scope by a bespoke session flag now resolve the
// shared --session flag through the same helper path as the bucket-A queries.
// --robot-digest reuses the attention resolver, so it is asserted through
// resolveRobotAttentionSession.
func TestResolveRobotSessionConvergedQueries(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(*cobra.Command) string
	}{
		{name: "events", resolve: resolveRobotEventsSession},
		{name: "attention", resolve: resolveRobotAttentionSession},
		{name: "digest", resolve: resolveRobotAttentionSession},
		{name: "overlay", resolve: resolveRobotOverlaySession},
		{name: "markdown", resolve: resolveRobotMarkdownSession},
		{name: "dismiss-alert", resolve: resolveRobotDismissSession},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRobotSessionTestCmd(t)
			if err := cmd.ParseFlags([]string{"--session=proj"}); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			if got := tt.resolve(cmd); got != "proj" {
				t.Errorf("%s resolver = %q, want %q", tt.name, got, "proj")
			}
		})
	}
}

// TestResolveRobotMailSessionPrecedence guards the --robot-mail convergence:
// the shared --session flag is the highest-precedence source, the positional
// argument is next, and the cwd/tmux inference remains the fallback. The
// inference itself is environment-dependent and predates this bead, so the
// regression guard here is precedence plus the explicit/inferred contract.
func TestResolveRobotMailSessionPrecedence(t *testing.T) {
	t.Run("--session flag wins over positional", func(t *testing.T) {
		cmd := newRobotSessionTestCmd(t)
		if err := cmd.ParseFlags([]string{"--session=flag"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		got, explicit := resolveRobotMailSession(cmd, []string{"positional"})
		if got != "flag" {
			t.Errorf("resolveRobotMailSession() = %q, want %q (flag wins)", got, "flag")
		}
		if !explicit {
			t.Errorf("resolveRobotMailSession() explicit = false, want true")
		}
	})

	t.Run("positional wins when no --session", func(t *testing.T) {
		cmd := newRobotSessionTestCmd(t)
		if err := cmd.ParseFlags(nil); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		got, explicit := resolveRobotMailSession(cmd, []string{"positional"})
		if got != "positional" {
			t.Errorf("resolveRobotMailSession() = %q, want %q (positional)", got, "positional")
		}
		if !explicit {
			t.Errorf("resolveRobotMailSession() explicit = false, want true")
		}
	})

	t.Run("no --session and no positional falls back to inference", func(t *testing.T) {
		cmd := newRobotSessionTestCmd(t)
		if err := cmd.ParseFlags(nil); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		got, explicit := resolveRobotMailSession(cmd, nil)
		// An inferred session is never reported as explicit; that is the
		// contract the dispatch branch relies on to pick the right scope
		// resolution path.
		if explicit {
			t.Errorf("resolveRobotMailSession() explicit = true, want false for inferred session")
		}
		if !tmux.IsInstalled() && got != "" {
			t.Errorf("resolveRobotMailSession() = %q, want empty when tmux is not installed", got)
		}
	})
}

// TestRemovedSessionFlagsAreGone enumerates rootCmd's registered flags and
// asserts none of the five deleted bespoke session-scoping names is present.
// This is the case that fails if one is quietly restored as an alias or a
// hidden flag.
func TestRemovedSessionFlagsAreGone(t *testing.T) {
	removed := []string{"events-session", "attention-session", "overlay-session", "md-session", "dismiss-session"}
	for _, name := range removed {
		if f := rootCmd.Flags().Lookup(name); f != nil {
			t.Errorf("flag --%s still registered on rootCmd (deprecated=%q); it must be deleted, not aliased", name, f.Deprecated)
		}
	}
}

// TestClassifyRobotExecuteErrorSuggestsSessionForRemovedSessionFlags guards the
// near-miss hint for the five deleted names: a caller reaching for one of them
// gets INVALID_FLAG with a hint naming --session, not the nearest surviving
// sibling (e.g. --md-sections for the old markdown session filter).
func TestClassifyRobotExecuteErrorSuggestsSessionForRemovedSessionFlags(t *testing.T) {
	removed := []string{"events-session", "attention-session", "overlay-session", "md-session", "dismiss-session"}
	for _, name := range removed {
		t.Run(name, func(t *testing.T) {
			code, hint := classifyRobotExecuteError(fmt.Errorf("unknown flag: --%s", name))
			if code != robot.ErrCodeInvalidFlag {
				t.Fatalf("code = %q, want INVALID_FLAG", code)
			}
			if !strings.Contains(hint, "--session") {
				t.Errorf("hint = %q, want a --session suggestion", hint)
			}
		})
	}
}
