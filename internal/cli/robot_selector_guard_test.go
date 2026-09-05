package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// newRobotPaneSelectorTestCmd mirrors the root command's pane selector flags
// on a throwaway command so the guard can be exercised against parsed flags
// without running the real dispatcher (which needs tmux and config).
func newRobotPaneSelectorTestCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	var pane, panes, historyPane, waitPanes string
	var dryRun bool
	cmd.Flags().StringVar(&pane, "pane", "", "")
	cmd.Flags().StringVar(&panes, "panes", "", "")
	cmd.Flags().StringVar(&historyPane, "history-pane", "", "")
	cmd.Flags().StringVar(&waitPanes, "wait-panes", "", "")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%v): %v", args, err)
	}
	return cmd
}

// TestValidateRobotPaneSelectorFlags_InterruptSingularPane is the ntm#308
// regression: `--robot-interrupt --pane=%12 --dry-run` used to be accepted
// with --pane silently ignored, so would_affect listed every agent pane. The
// singular flag must now fail closed before dispatch and point at --panes.
func TestValidateRobotPaneSelectorFlags_InterruptSingularPane(t *testing.T) {
	cmd := newRobotPaneSelectorTestCmd(t, "--pane=%12", "--dry-run")
	err := validateRobotPaneSelectorFlags(cmd, "robot-interrupt")
	if err == nil {
		t.Fatal("--pane with --robot-interrupt was accepted; it must fail closed")
	}
	var selectorErr *robotPaneSelectorFlagError
	if !errors.As(err, &selectorErr) {
		t.Fatalf("error type = %T, want *robotPaneSelectorFlagError", err)
	}
	if selectorErr.flag != "pane" || selectorErr.command != "robot-interrupt" {
		t.Fatalf("error = %+v, want flag=pane command=robot-interrupt", selectorErr)
	}
	if !strings.Contains(err.Error(), "--pane is not supported with --robot-interrupt") {
		t.Errorf("error message %q does not name the rejected pair", err.Error())
	}
	if !strings.Contains(selectorErr.hint, "--panes=%12") {
		t.Errorf("hint %q must point at --panes with the supplied selector", selectorErr.hint)
	}

	// The plural form is the supported selector and must pass the guard.
	plural := newRobotPaneSelectorTestCmd(t, "--panes=%12", "--dry-run")
	if err := validateRobotPaneSelectorFlags(plural, "robot-interrupt"); err != nil {
		t.Fatalf("--panes with --robot-interrupt rejected: %v", err)
	}
}

func TestValidateRobotPaneSelectorFlags_Matrix(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		args     []string
		wantErr  bool
		wantFlag string
		wantHint string
	}{
		{name: "send accepts singular", command: "robot-send", args: []string{"--pane=2"}},
		{name: "send accepts plural", command: "robot-send", args: []string{"--panes=1,2"}},
		{name: "history accepts singular", command: "robot-history", args: []string{"--pane=1.0"}},
		{name: "history rejects plural", command: "robot-history", args: []string{"--panes=1,2"}, wantErr: true, wantFlag: "panes", wantHint: "--pane=<one selector>"},
		{name: "history accepts its prefixed alias", command: "robot-history", args: []string{"--history-pane=%3"}},
		{name: "send rejects history alias", command: "robot-send", args: []string{"--history-pane=%3"}, wantErr: true, wantFlag: "history-pane", wantHint: "--pane=%3"},
		{name: "wait accepts plural", command: "robot-wait", args: []string{"--panes=1"}},
		{name: "wait accepts its prefixed alias", command: "robot-wait", args: []string{"--wait-panes=1"}},
		{name: "wait rejects singular", command: "robot-wait", args: []string{"--pane=1"}, wantErr: true, wantFlag: "pane", wantHint: "--panes=1"},
		{name: "interrupt rejects wait alias", command: "robot-interrupt", args: []string{"--wait-panes=1"}, wantErr: true, wantFlag: "wait-panes", wantHint: "--panes=1"},
		{name: "restart-pane rejects singular", command: "robot-restart-pane", args: []string{"--pane=%7"}, wantErr: true, wantFlag: "pane", wantHint: "--panes=%7"},
		{name: "kill-pane rejects singular", command: "robot-kill-pane", args: []string{"--pane=%7"}, wantErr: true, wantFlag: "pane", wantHint: "--panes=%7"},
		{name: "status rejects plural", command: "robot-status", args: []string{"--panes=1"}, wantErr: true, wantFlag: "panes", wantHint: "--panes is only read by"},
		{name: "switch-account rejects singular", command: "robot-switch-account", args: []string{"--pane=1"}, wantErr: true, wantFlag: "pane", wantHint: "--pane is only read by"},
		{name: "no selector passes", command: "robot-interrupt", args: []string{"--dry-run"}},
		{name: "unknown surface is not validated", command: "robot-not-a-real-surface", args: []string{"--pane=1"}},
		{name: "empty command is not validated", command: "", args: []string{"--pane=1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRobotPaneSelectorTestCmd(t, tt.args...)
			err := validateRobotPaneSelectorFlags(cmd, tt.command)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected rejection: %v", err)
				}
				return
			}
			var selectorErr *robotPaneSelectorFlagError
			if !errors.As(err, &selectorErr) {
				t.Fatalf("err = %v (%T), want *robotPaneSelectorFlagError", err, err)
			}
			if selectorErr.flag != tt.wantFlag {
				t.Errorf("rejected flag = %q, want %q", selectorErr.flag, tt.wantFlag)
			}
			if !strings.Contains(selectorErr.hint, tt.wantHint) {
				t.Errorf("hint = %q, want it to contain %q", selectorErr.hint, tt.wantHint)
			}
		})
	}
}

// TestRobotPaneSelectorGuardFlagsAreLive guards the guard: every flag it
// inspects must exist on the real root command, otherwise Changed() could
// never be true and the check would silently do nothing.
func TestRobotPaneSelectorGuardFlagsAreLive(t *testing.T) {
	for _, selector := range robotPaneSelectorFlags {
		if liveRootFlag(selector.name) == nil {
			t.Errorf("guarded selector flag --%s is not registered on the root command", selector.name)
		}
		if liveRootFlag(selector.canonical) == nil {
			t.Errorf("canonical selector flag --%s is not registered on the root command", selector.canonical)
		}
	}
}

// TestRobotPaneSelectorRegistryCoversConsumers pins the registry against the
// dispatcher: every robot surface whose handler in root.go reads --panes or
// --pane must declare it, otherwise the fail-closed guard would reject a
// selector the surface actually honours.
func TestRobotPaneSelectorRegistryCoversConsumers(t *testing.T) {
	panesConsumers := []string{
		"robot-activity", "robot-wait", "robot-tail", "robot-watch-bead", "robot-errors",
		"robot-is-working", "robot-agent-health", "robot-smart-restart", "robot-monitor",
		"robot-send", "robot-ack", "robot-interrupt", "robot-dialogs", "robot-answer-dialog",
		"robot-kill-pane", "robot-exit-cli", "robot-kill-agent", "robot-restart-pane",
		"robot-probe", "robot-rano-stats",
	}
	for _, command := range panesConsumers {
		accepts, known := robot.SurfaceParameterSupport(command, "panes")
		if !known {
			t.Errorf("%s reads --panes but has no registry surface", command)
			continue
		}
		if !accepts {
			t.Errorf("%s reads --panes but its registry surface does not declare it", command)
		}
	}
	for _, command := range []string{"robot-send", "robot-history"} {
		accepts, known := robot.SurfaceParameterSupport(command, "pane")
		if !known || !accepts {
			t.Errorf("%s reads --pane but its registry surface does not declare it (known=%v accepts=%v)", command, known, accepts)
		}
	}
	// --robot-switch-account rejects pane targeting inside its handler; the
	// registry must keep agreeing so the guard fails it closed earlier.
	if accepts, known := robot.SurfaceParameterSupport("robot-switch-account", "pane"); !known || accepts {
		t.Errorf("robot-switch-account must be known and must not declare --pane (known=%v accepts=%v)", known, accepts)
	}
}
