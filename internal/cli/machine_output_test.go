package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/testutil"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// captureProcessStreams runs fn with os.Stdout and os.Stderr redirected to
// pipes and returns what each one received.
func captureProcessStreams(t *testing.T, fn func()) (string, string) {
	t.Helper()
	origStdout, origStderr := os.Stdout, os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stdoutBuf, stdoutR)
		close(stdoutDone)
	}()
	go func() {
		_, _ = io.Copy(&stderrBuf, stderrR)
		close(stderrDone)
	}()
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		os.Stdout, os.Stderr = origStdout, origStderr
		_ = stdoutW.Close()
		_ = stderrW.Close()
		<-stdoutDone
		<-stderrDone
	}
	t.Cleanup(restore)
	os.Stdout, os.Stderr = stdoutW, stderrW
	fn()
	restore()
	return stdoutBuf.String(), stderrBuf.String()
}

func hasAgentMailWarning(warnings []string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, "Agent Mail") {
			return true
		}
	}
	return false
}

// Robot and --json invocations own stdout as a single JSON document, and the
// robot contract leaves stderr empty. When Agent Mail is unreachable during a
// restart's identity refresh, the warning must come back for the restart
// envelope instead of reaching either stream. Human invocations keep printing
// it.
func TestRegisterRelaunchedPanesMachineOutputReturnsWarningsInsteadOfPrinting(t *testing.T) {
	isolateIdentityDirs(t)
	t.Chdir(t.TempDir())
	t.Setenv("AGENT_MAIL_URL", testutil.ClosedAgentMailURL)
	t.Setenv("AGENT_MAIL_TOKEN", "")
	installFakeBadgeTmux(t, &fakeBadgeTmux{})
	oldProbe := spawnIdentityPaneProbe
	spawnIdentityPaneProbe = func(context.Context, string) ([]tmux.Pane, error) {
		return nil, errors.New("no tmux server in this test")
	}
	t.Cleanup(func() { spawnIdentityPaneProbe = oldProbe })
	oldCfg, oldJSON := cfg, jsonOutput
	t.Cleanup(func() { cfg, jsonOutput = oldCfg, oldJSON })
	cfg = config.Default()
	cfg.AgentMail.Enabled = true
	cfg.AgentMail.AutoRegister = true
	cfg.AgentMail.URL = testutil.ClosedAgentMailURL
	jsonOutput = false

	session := "relaunch-machine-output-test"
	panes := []robot.RestartedAgentPane{{PaneIndex: 1, PaneID: "%9", PaneTitle: session + "__cc_1", AgentType: "cc"}}

	var warnings []string
	restoreDiagnostics := suppressRobotDiagnostics(rootCmd)
	stdout, stderr := captureProcessStreams(t, func() {
		warnings = registerRelaunchedPanes(withMachineOutput(t.Context()), session, panes)
	})
	restoreDiagnostics()
	if stdout != "" || stderr != "" {
		t.Fatalf("machine-output relaunch wrote stdout=%q stderr=%q, want both empty", stdout, stderr)
	}
	if !hasAgentMailWarning(warnings) {
		t.Fatalf("machine-output relaunch warnings = %q, want the Agent Mail failure for the envelope", warnings)
	}

	var humanWarnings []string
	stdout, _ = captureProcessStreams(t, func() {
		humanWarnings = registerRelaunchedPanes(t.Context(), session, panes)
	})
	if humanWarnings != nil {
		t.Fatalf("human relaunch returned warnings %q, want them printed instead", humanWarnings)
	}
	if !strings.Contains(stdout, "Agent Mail") {
		t.Fatalf("human relaunch stdout = %q, want the Agent Mail warning", stdout)
	}
}

// Robot sends run the same dcg guard as `ntm send`. A blocked command whose
// audit log cannot be opened must not print the human notice into a robot
// response; the envelope already reports the block.
func TestLogDCGBlockedKeepsMachineOutputStdoutClean(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatalf("write placeholder file: %v", err)
	}
	oldCfg, oldJSON := cfg, jsonOutput
	t.Cleanup(func() { cfg, jsonOutput = oldCfg, oldJSON })
	cfg = config.Default()
	cfg.Integrations.DCG.AuditLog = filepath.Join(notADir, "dcg-audit.jsonl")
	jsonOutput = false

	panes := []tmux.Pane{{ID: "%1", Index: 1, Type: tmux.AgentClaude, Title: "demo__cc_1"}}
	blocked := &tools.BlockedCommand{Command: "example-blocked-command", Reason: "blocked by test rule"}

	stdout, _ := captureProcessStreams(t, func() {
		logDCGBlocked(withMachineOutput(t.Context()), "example-blocked-command", "demo", panes, blocked)
	})
	if stdout != "" {
		t.Fatalf("machine-output dcg audit failure wrote stdout=%q, want none", stdout)
	}

	stdout, _ = captureProcessStreams(t, func() {
		logDCGBlocked(t.Context(), "example-blocked-command", "demo", panes, blocked)
	})
	if !strings.Contains(stdout, "DCG audit log unavailable") {
		t.Fatalf("human dcg audit failure stdout = %q, want the notice", stdout)
	}
}
