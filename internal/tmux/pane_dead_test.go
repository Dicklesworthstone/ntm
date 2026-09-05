package tmux

import (
	"strings"
	"testing"
	"time"
)

// ntm#311: #{pane_dead} is the last positional field of every list-panes
// format ntm parses. These tests pin its position so a dead pane is
// recognised and no earlier field (PID, window, agent type, service tag)
// shifts when it is present or absent.

// fullPaneLine builds the current 13-field GetPanes format line.
func fullPaneLine(id, index, title, command, pid, window, recordedType, acfsService, ntmService, dead string) string {
	return strings.Join([]string{
		id, index, title, command, "80", "24", "1", pid, window,
		recordedType, acfsService, ntmService, dead,
	}, FieldSeparator)
}

func TestParsePaneLine_PaneDeadFlag(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneLine(fullPaneLine("%2", "1", "sess__cod_2", "codex", "4242", "3", "codex", "", "", "1"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if !pane.Dead {
		t.Fatal("pane_dead=1 not parsed as Dead")
	}
	// Nothing before the new trailing field may have shifted.
	if pane.ID != "%2" || pane.Index != 1 || pane.PID != 4242 || pane.WindowIndex != 3 || pane.Type != AgentCodex {
		t.Fatalf("earlier fields shifted: %+v", pane)
	}
	if pane.IsServicePane() {
		t.Fatalf("empty service tags parsed as a service pane: %+v", pane)
	}

	live, err := parsePaneLine(fullPaneLine("%1", "0", "sess__cc_1", "claude", "4100", "3", "", "", "", "0"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine live: %v", err)
	}
	if live.Dead {
		t.Fatalf("pane_dead=0 parsed as Dead: %+v", live)
	}
}

func TestParsePaneLine_PaneDeadDoesNotShiftServiceTags(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneLine(fullPaneLine("%5", "0", "cm", "cm", "9", "0", "", "cm", "", "1"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if !pane.Dead || !pane.IsServicePane() || pane.Service != "cm" || pane.ServiceManager != "acfs" {
		t.Fatalf("service tag or dead flag misparsed: %+v", pane)
	}

	// A dead-looking value in the @ntm_service slot must stay a service tag,
	// not be read as pane_dead.
	pane, err = parsePaneLine(fullPaneLine("%6", "0", "one", "sh", "10", "0", "", "", "1", "0"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Dead {
		t.Fatalf("@ntm_service value read as pane_dead: %+v", pane)
	}
	if pane.Service != "1" || pane.ServiceManager != "ntm" {
		t.Fatalf("@ntm_service tag misparsed: %+v", pane)
	}
}

// Pre-#311 (12-field) and pre-#305 (10-field) lines still parse, as live.
func TestParsePaneLine_OlderFormatsParseAsLive(t *testing.T) {
	t.Parallel()

	twelve, err := parsePaneLine(servicePaneLine("%0", "1", "cm", "cm", "4242", "0", "", "cm", ""), FieldSeparator)
	if err != nil {
		t.Fatalf("12-field line: %v", err)
	}
	if twelve.Dead || twelve.Service != "cm" || twelve.PID != 4242 {
		t.Fatalf("12-field line misparsed: %+v", twelve)
	}
	ten, err := parsePaneLine(paneLine("%1", "0", "sess__cc_2", "claude", "77", "0", ""), FieldSeparator)
	if err != nil {
		t.Fatalf("10-field line: %v", err)
	}
	if ten.Dead || ten.PID != 77 || ten.Type != AgentClaude {
		t.Fatalf("10-field line misparsed: %+v", ten)
	}

	if _, err := parsePaneLine(fullPaneLine("%1", "0", "t", "sh", "1", "0", "", "", "", "0")+FieldSeparator+"extra", FieldSeparator); err == nil {
		t.Fatal("14-field GetPanes line accepted; the field-count guard no longer bounds the format")
	}
}

// The all-sessions and activity formats carry pane_dead in the same trailing
// slot after their own leading/inserted fields.
func TestParsePaneFromParts_PaneDeadInSecondaryFields(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneFromParts(
		[]string{"%3", "0", "sess__gmi_1", "gemini", "80", "24", "0"},
		[]string{"555", "2", "", "", "", "1"},
	)
	if err != nil {
		t.Fatalf("parsePaneFromParts: %v", err)
	}
	if !pane.Dead || pane.PID != 555 || pane.WindowIndex != 2 || pane.Type != AgentGemini {
		t.Fatalf("secondary fields misparsed: %+v", pane)
	}
	if _, err := parsePaneFromParts(
		[]string{"%3", "0", "t", "sh", "80", "24", "0"},
		[]string{"555", "2", "", "", "", "1", "extra"},
	); err == nil {
		t.Fatal("7 secondary fields accepted; the bound no longer matches the producers")
	}
}

// TestGetPanes_ReportsDeadPane drives real tmux (isolated server): a pane whose
// process exited under remain-on-exit must come back Dead, and tmux reports an
// empty current path for it, which is the ntm#311 precondition.
func TestGetPanes_ReportsDeadPane(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real tmux session integration in short mode")
	}
	session := createTestSession(t)

	if err := DefaultClient.RunSilent("set-option", "-w", "-t", SessionOptionTarget(session), "remain-on-exit", "on"); err != nil {
		t.Fatalf("set remain-on-exit: %v", err)
	}
	if err := DefaultClient.RunSilent("split-window", "-d", "-t", SessionOptionTarget(session), "exit 1"); err != nil {
		t.Fatalf("split-window: %v", err)
	}

	var dead, live *Pane
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		panes, err := GetPanes(session)
		if err != nil {
			t.Fatalf("GetPanes: %v", err)
		}
		dead, live = nil, nil
		for i := range panes {
			if panes[i].Dead {
				dead = &panes[i]
			} else {
				live = &panes[i]
			}
		}
		if dead != nil && live != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if dead == nil || live == nil {
		t.Fatalf("expected one dead and one live pane after the exited split, got dead=%v live=%v", dead != nil, live != nil)
	}

	out, err := DefaultClient.Run("display-message", "-p", "-t", ExactTarget(dead.ID), "#{pane_dead} [#{pane_current_path}]")
	if err != nil {
		t.Fatalf("display-message on dead pane: %v", err)
	}
	t.Logf("dead pane %s: %s", dead.ID, strings.TrimSpace(out))
	if !strings.HasPrefix(strings.TrimSpace(out), "1 ") {
		t.Fatalf("tmux does not report %s as dead: %q", dead.ID, out)
	}

	all, err := GetAllPanes()
	if err != nil {
		t.Fatalf("GetAllPanes: %v", err)
	}
	found := false
	for _, p := range all[session] {
		if p.ID == dead.ID {
			found = true
			if !p.Dead {
				t.Fatalf("GetAllPanes lost the dead flag for %s: %+v", p.ID, p)
			}
		}
	}
	if !found {
		t.Fatalf("GetAllPanes did not list %s under %s", dead.ID, session)
	}

	activity, err := DefaultClient.GetPanesWithActivityContext(t.Context(), session)
	if err != nil {
		t.Fatalf("GetPanesWithActivityContext: %v", err)
	}
	for _, pa := range activity {
		if pa.Pane.ID == dead.ID && !pa.Pane.Dead {
			t.Fatalf("GetPanesWithActivityContext lost the dead flag for %s: %+v", pa.Pane.ID, pa.Pane)
		}
	}
}
