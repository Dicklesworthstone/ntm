package cli

// End-to-end regression for ntm#312 on the isolated tmux server (TestMain):
// `ntm mapping --session` reconciles identities, publishes badges into real
// pane options and the window border format, reports drift, and withdraws
// everything again once badges are disabled.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestMappingCommand_PublishesRealPaneBadgesAndReportsDrift(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)

	workDir := t.TempDir()
	session := fmt.Sprintf("ntm-test-badges-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, workDir); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })
	if _, err := tmux.DefaultClient.Run("split-window", "-d", "-t", tmux.SessionOptionTarget(session), "-c", workDir); err != nil {
		t.Fatalf("split-window: %v", err)
	}
	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) != 2 {
		t.Fatalf("panes = %d (%v)", len(panes), err)
	}
	userPane, agentPane := panes[0], panes[1]
	if _, err := tmux.DefaultClient.Run("select-pane", "-t", userPane.ID, "-T", session+"__user"); err != nil {
		t.Fatal(err)
	}
	if _, err := tmux.DefaultClient.Run("select-pane", "-t", agentPane.ID, "-T", session+"__cc_1"); err != nil {
		t.Fatal(err)
	}

	// Put a foreground process in the agent pane so its lifecycle reads as
	// running (a bare shell is "exited" by contract).
	if err := tmux.SendKeys(agentPane.ID, "exec /bin/cat", true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := tmux.GetPanes(session)
		if err == nil && len(current) == 2 && strings.HasSuffix(current[1].Command, "cat") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent pane never started cat: %+v (%v)", current, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	registry := agentmail.NewSessionAgentRegistry(session, workDir)
	registry.AddAgent(session+"__cc_1", agentPane.ID, "BlueLake")
	registry.SetPanePID(agentPane.ID, agentPane.PID)
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatal(err)
	}
	// A later process rewrote the identity file with another name: the
	// badge must keep BlueLake and flag the drift.
	if _, err := agentmail.WriteIdentity(workDir, agentPane.ID, "RedFox"); err != nil {
		t.Fatal(err)
	}

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	runMapping := func() agentMappingOutput {
		t.Helper()
		cmd := newMappingCmd()
		cmd.SetContext(context.Background())
		cmd.SetArgs([]string{"--session", session})
		out, runErr := captureStdout(t, cmd.Execute)
		if runErr != nil {
			t.Fatalf("ntm mapping: %v\n%s", runErr, out)
		}
		var decoded agentMappingOutput
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("decode mapping JSON: %v\n%s", err, out)
		}
		return decoded
	}

	first := runMapping()
	if first.Count != 1 || first.Entries[0].Name != "BlueLake" || first.Entries[0].Identity == nil {
		t.Fatalf("mapping = %+v", first)
	}
	identity := first.Entries[0].Identity
	if identity.AssignmentState != agentmail.PaneAssignmentCurrent || identity.ObservationState != agentmail.PaneObservationNameDisagreement ||
		identity.ResolvedName != "RedFox" || identity.Label != "[BlueLake!]" || !identity.Published {
		t.Fatalf("identity = %+v", identity)
	}
	if len(first.Discrepancies) != 1 || first.Discrepancies[0].PaneID != agentPane.ID {
		t.Fatalf("discrepancies = %+v", first.Discrepancies)
	}
	if first.Badges == nil || !first.Badges.Enabled || first.Badges.Published != 1 || first.Badges.WindowsPrepared != 1 {
		t.Fatalf("badges = %+v", first.Badges)
	}

	// Real pane options and border format.
	badge, err := tmux.DefaultClient.ReadPaneBadgeContext(context.Background(), agentPane.ID)
	if err != nil {
		t.Fatal(err)
	}
	if badge.Name != "BlueLake" || badge.State != "name-disagreement" || badge.Label != "[BlueLake!]" || badge.Lifecycle == "" {
		t.Fatalf("pane options = %+v", badge)
	}
	if userBadge, _ := tmux.DefaultClient.ReadPaneBadgeContext(context.Background(), userPane.ID); userBadge != (tmux.PaneBadge{}) {
		t.Fatalf("user pane received badge options: %+v", userBadge)
	}
	format, err := tmux.DefaultClient.Run("show-options", "-w", "-v", "-t", tmux.SessionOptionTarget(session), "pane-border-format")
	if err != nil || !strings.Contains(format, tmux.PaneBorderBadgeFragment) {
		t.Fatalf("pane-border-format = %q (%v)", format, err)
	}
	rendered, err := tmux.DefaultClient.Run("display-message", "-p", "-t", agentPane.ID, format)
	if err != nil || !strings.Contains(rendered, session+"__cc_1") || !strings.HasSuffix(rendered, " [BlueLake!]") {
		t.Fatalf("rendered border = %q (%v): badge must sit next to the existing title", rendered, err)
	}

	// Disable: `ntm mapping` withdraws the options and restores the border.
	setBadgeConfig(t, false)
	second := runMapping()
	if second.Badges == nil || second.Badges.Enabled || second.Badges.Cleared != 1 || second.Badges.WindowsRestored != 1 {
		t.Fatalf("disabled badges = %+v", second.Badges)
	}
	if len(second.Discrepancies) != 1 {
		t.Fatalf("drift must still be reported with badges off: %+v", second.Discrepancies)
	}
	if badge, _ := tmux.DefaultClient.ReadPaneBadgeContext(context.Background(), agentPane.ID); badge != (tmux.PaneBadge{}) {
		t.Fatalf("pane options not withdrawn: %+v", badge)
	}
	format, _ = tmux.DefaultClient.Run("show-options", "-w", "-q", "-v", "-t", tmux.SessionOptionTarget(session), "pane-border-format")
	if strings.TrimSpace(format) != "" {
		t.Fatalf("pane-border-format not restored to inheritance: %q", format)
	}
}
