package robot

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// selectorSurfaceSession is a real (isolated-socket) tmux session with two
// windows: window A split into two panes, window B with one pane. Every pane is
// a recorded claude agent whose scrollback carries a bead mention and an error
// line tagged with the pane's own ID, so each surface's output shows exactly
// which panes it read.
type selectorSurfaceSession struct {
	name    string
	windowA int
	windowB int
	a0, a1  tmux.Pane // window A, panes in topology order
	b0      tmux.Pane // window B's only pane
}

const selectorSurfaceBead = "bd-sel329"

func newSelectorSurfaceSession(t *testing.T) selectorSurfaceSession {
	t.Helper()
	testutil.RequireTmuxThrottled(t)
	session := fmt.Sprintf("ntm-selector-surfaces-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, ""); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	if _, err := tmux.DefaultClient.Run("split-window", "-d", "-t", session); err != nil {
		t.Fatalf("split first window: %v", err)
	}
	if _, err := tmux.DefaultClient.Run("new-window", "-d", "-t", session); err != nil {
		t.Fatalf("create second window: %v", err)
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 3 {
		t.Fatalf("setup produced %d panes, want 3: %+v", len(panes), panes)
	}
	for _, pane := range panes {
		// Pane IDs contain '%', so pass them as printf arguments, never as
		// part of the format string.
		script := fmt.Sprintf("printf '%%s\\n' 'working on %s in %s' 'fatal error: marker %s'; exec cat", selectorSurfaceBead, pane.ID, pane.ID)
		if _, err := tmux.DefaultClient.Run("respawn-pane", "-k", "-t", pane.ID, "sh", "-c", script); err != nil {
			t.Fatalf("respawn %s: %v", pane.ID, err)
		}
		if err := tmux.DefaultClient.SetPaneAgentType(pane.ID, tmux.AgentClaude); err != nil {
			t.Fatalf("record agent type on %s: %v", pane.ID, err)
		}
	}
	for _, pane := range panes {
		deadline := time.Now().Add(10 * time.Second)
		for {
			out, _ := tmux.CapturePaneOutput(pane.ID, 50)
			if strings.Contains(out, "marker "+pane.ID) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("pane %s never printed its marker; captured %q", pane.ID, out)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	panes, err = tmux.GetPanes(session)
	if err != nil {
		t.Fatal(err)
	}
	ordered := tmux.SortPanesByTopology(panes)
	s := selectorSurfaceSession{name: session, a0: ordered[0], a1: ordered[1], b0: ordered[2]}
	s.windowA, s.windowB = s.a0.WindowIndex, s.b0.WindowIndex
	if s.a1.WindowIndex != s.windowA || s.windowA == s.windowB {
		t.Fatalf("unexpected topology: %+v", ordered)
	}
	return s
}

func (s selectorSurfaceSession) ref(p tmux.Pane) string {
	return fmt.Sprintf("%d.%d", p.WindowIndex, p.Index)
}

type selectorSurfaceCase struct {
	name      string
	selectors []string
	wantIDs   []string
}

// cases returns the positive selector matrix. The bare-N case is the
// multi-window convention: N selects window N. The old integer-only filters
// matched the window-local pane index instead, so "windowA" hit a0 plus b0
// (both pane index 0) rather than window A's two panes.
func (s selectorSurfaceSession) cases() []selectorSurfaceCase {
	return []selectorSurfaceCase{
		{"window.pane names the second pane of a split window", []string{s.ref(s.a1)}, []string{s.a1.ID}},
		{"pane id", []string{s.b0.ID}, []string{s.b0.ID}},
		{"bare N selects a whole window", []string{fmt.Sprint(s.windowA)}, []string{s.a0.ID, s.a1.ID}},
		{"aliases resolve once", []string{s.a1.ID, s.ref(s.a1)}, []string{s.a1.ID}},
	}
}

func (s selectorSurfaceSession) failureCases() []struct {
	name      string
	selectors []string
	wantCode  string
} {
	return []struct {
		name      string
		selectors []string
		wantCode  string
	}{
		{"unknown window.pane", []string{s.ref(s.a0), fmt.Sprintf("%d.9", s.windowA)}, ErrCodePaneNotFound},
		{"unknown pane id", []string{"%999999"}, ErrCodePaneNotFound},
		{"malformed selector", []string{fmt.Sprintf("%d.x", s.windowA)}, ErrCodeInvalidFlag},
	}
}

func sortedUnique(ids []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func TestWatchBeadAcceptsSharedPaneSelectors(t *testing.T) {
	s := newSelectorSurfaceSession(t)
	for _, tc := range s.cases() {
		t.Run(tc.name, func(t *testing.T) {
			out, err := GetWatchBead(WatchBeadOptions{Session: s.name, BeadID: selectorSurfaceBead, PaneSelectors: tc.selectors, Lines: 50})
			if err != nil || !out.Success {
				t.Fatalf("GetWatchBead(%v) = %+v, %v", tc.selectors, out.RobotResponse, err)
			}
			var ids []string
			for _, mention := range out.Mentions {
				ids = append(ids, mention.PaneID)
				if !strings.Contains(mention.Line, mention.PaneID) {
					t.Fatalf("mention %+v attributed to the wrong pane", mention)
				}
				if want := s.ref(tmuxPaneByID(t, s, mention.PaneID)); mention.PaneRef != want {
					t.Fatalf("pane_ref = %q, want %q", mention.PaneRef, want)
				}
			}
			if got, want := sortedUnique(ids), sortedUnique(tc.wantIDs); !reflect.DeepEqual(got, want) {
				t.Fatalf("selectors %v scanned panes %v, want %v", tc.selectors, got, want)
			}
			if out.PanesScanned != len(tc.wantIDs) {
				t.Fatalf("panes_scanned = %d, want %d", out.PanesScanned, len(tc.wantIDs))
			}
		})
	}
	for _, tc := range s.failureCases() {
		t.Run(tc.name, func(t *testing.T) {
			out, err := GetWatchBead(WatchBeadOptions{Session: s.name, BeadID: selectorSurfaceBead, PaneSelectors: tc.selectors, Lines: 50})
			if err != nil || out.Success || out.ErrorCode != tc.wantCode || len(out.Mentions) != 0 {
				t.Fatalf("GetWatchBead(%v) = %+v (%v), want %s with no mentions", tc.selectors, out, err, tc.wantCode)
			}
		})
	}
}

func TestErrorsAcceptsSharedPaneSelectors(t *testing.T) {
	s := newSelectorSurfaceSession(t)
	for _, tc := range s.cases() {
		t.Run(tc.name, func(t *testing.T) {
			out, err := GetErrors(ErrorsOptions{Session: s.name, Panes: tc.selectors, Lines: 50})
			if err != nil || !out.Success {
				t.Fatalf("GetErrors(%v) = %+v, %v", tc.selectors, out.RobotResponse, err)
			}
			var ids []string
			for _, entry := range out.Errors {
				ids = append(ids, entry.PaneID)
				if !strings.Contains(entry.Content, entry.PaneID) {
					t.Fatalf("error %+v attributed to the wrong pane", entry)
				}
			}
			if got, want := sortedUnique(ids), sortedUnique(tc.wantIDs); !reflect.DeepEqual(got, want) {
				t.Fatalf("selectors %v searched panes %v, want %v", tc.selectors, got, want)
			}
		})
	}
	for _, tc := range s.failureCases() {
		t.Run(tc.name, func(t *testing.T) {
			out, err := GetErrors(ErrorsOptions{Session: s.name, Panes: tc.selectors, Lines: 50})
			if err != nil || out.Success || out.ErrorCode != tc.wantCode || len(out.Errors) != 0 {
				t.Fatalf("GetErrors(%v) = %+v (%v), want %s with no errors", tc.selectors, out, err, tc.wantCode)
			}
		})
	}
}

func TestMonitorAcceptsSharedPaneSelectors(t *testing.T) {
	s := newSelectorSurfaceSession(t)
	for _, tc := range s.cases() {
		t.Run(tc.name, func(t *testing.T) {
			m := &Monitor{config: MonitorConfig{Session: s.name, PaneSelectors: tc.selectors}}
			panes, err := m.getPanesToCheck()
			if err != nil {
				t.Fatalf("getPanesToCheck(%v): %v", tc.selectors, err)
			}
			var ids []string
			for _, pane := range panes {
				ids = append(ids, pane.ID)
			}
			if got, want := sortedUnique(ids), sortedUnique(tc.wantIDs); !reflect.DeepEqual(got, want) {
				t.Fatalf("selectors %v monitor panes %v, want %v", tc.selectors, got, want)
			}
		})
	}
	for _, tc := range s.failureCases() {
		t.Run(tc.name, func(t *testing.T) {
			m := &Monitor{config: MonitorConfig{Session: s.name, PaneSelectors: tc.selectors}}
			if panes, err := m.getPanesToCheck(); err == nil {
				t.Fatalf("getPanesToCheck(%v) = %v, want an error", tc.selectors, panes)
			}
			// The CLI entry point must refuse to start rather than run a
			// monitor over the wrong panes.
			stdout, err := captureStdout(t, func() error {
				return PrintMonitor(MonitorConfig{Session: s.name, PaneSelectors: tc.selectors, Interval: time.Second})
			})
			var exitErr *ProcessExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("PrintMonitor(%v) err = %T %v, want exit-1 ProcessExitError", tc.selectors, err, err)
			}
			var response RobotResponse
			if err := json.Unmarshal([]byte(stdout), &response); err != nil {
				t.Fatalf("failure is not JSON: %v\noutput=%q", err, stdout)
			}
			if response.Success || response.ErrorCode != tc.wantCode {
				t.Fatalf("PrintMonitor(%v) response = %+v, want %s", tc.selectors, response, tc.wantCode)
			}
		})
	}
}

func tmuxPaneByID(t *testing.T, s selectorSurfaceSession, id string) tmux.Pane {
	t.Helper()
	for _, pane := range []tmux.Pane{s.a0, s.a1, s.b0} {
		if pane.ID == id {
			return pane
		}
	}
	t.Fatalf("pane %s is not part of the fixture session", id)
	return tmux.Pane{}
}

// Restart matching treats an unparseable token as matching nothing, so a
// malformed --panes list used to succeed as a silent no-op (or, mixed with a
// valid selector, restart only the valid part). The engine must reject it
// before touching any pane.
func TestRestartPaneRejectsMalformedPaneSelectors(t *testing.T) {
	s := newSelectorSurfaceSession(t)
	before, err := tmux.GetPanes(s.name)
	if err != nil {
		t.Fatal(err)
	}
	for _, selectors := range [][]string{
		{fmt.Sprintf("%d.x", s.windowA)},
		{"all"},
		{"pane-1"},
		{""},
	} {
		out, err := GetRestartPaneContext(t.Context(), RestartPaneOptions{Session: s.name, Panes: selectors})
		if err != nil {
			t.Fatalf("GetRestartPaneContext(%q) error: %v", selectors, err)
		}
		if out.Success || out.ErrorCode != ErrCodeInvalidFlag || len(out.Restarted) != 0 {
			t.Fatalf("GetRestartPaneContext(%q) = %+v, want INVALID_FLAG with nothing restarted", selectors, out)
		}
	}
	after, err := tmux.GetPanes(s.name)
	if err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].PID != after[i].PID {
			t.Fatalf("pane %s changed (%d -> %d) although every request was rejected", before[i].ID, before[i].PID, after[i].PID)
		}
	}
}
