package robot

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// The public probe surface runs its real selection, stimulus, capture,
// cleanup and escalation against a controllable tmux transport.
type probeExecutionClient struct {
	panes       []tmux.Pane
	calls       []string
	capture     func(string, int) (string, error)
	discover    func(int) ([]tmux.Pane, error)
	send        func(string, string) error
	captures    int
	discoveries int
}

func newProbeExecutionClient(t *testing.T, panes ...tmux.Pane) *probeExecutionClient {
	t.Helper()
	client := &probeExecutionClient{panes: panes}
	client.capture = func(_ string, n int) (string, error) {
		if n%2 == 1 {
			return "baseline", nil
		}
		return "baseline ", nil
	}
	old := CurrentTmuxClient
	CurrentTmuxClient = client
	t.Cleanup(func() { CurrentTmuxClient = old })
	return client
}
func executionPane(id string, index int) tmux.Pane {
	return tmux.Pane{ID: id, Index: index, Type: tmux.AgentClaude, Command: "claude", PID: 100 + index}
}
func (c *probeExecutionClient) SessionExists(string) bool { return true }
func (c *probeExecutionClient) GetPanes(string) ([]tmux.Pane, error) {
	c.discoveries++
	if c.discover != nil {
		return c.discover(c.discoveries)
	}
	return append([]tmux.Pane(nil), c.panes...), nil
}
func (c *probeExecutionClient) CaptureForStatusDetection(target string) (string, error) {
	c.calls = append(c.calls, "capture:"+target)
	c.captures++
	return c.capture(target, c.captures)
}
func (c *probeExecutionClient) CapturePaneOutput(target string, _ int) (string, error) {
	c.calls = append(c.calls, "tail:"+target)
	c.captures++
	return c.capture(target, c.captures)
}
func (c *probeExecutionClient) SendKeys(target, text string, enter bool) error {
	if enter {
		return errors.New("probe must not submit Enter")
	}
	return c.input(target, text)
}
func (c *probeExecutionClient) SendKeyName(target, key string) error { return c.input(target, key) }
func (c *probeExecutionClient) SendInterrupt(target string) error    { return c.input(target, "C-c") }
func (c *probeExecutionClient) input(target, text string) error {
	c.calls = append(c.calls, "input:"+target+":"+text)
	if c.send != nil {
		return c.send(target, text)
	}
	return nil
}
func runExecutionProbe(method ProbeMethod, aggressive bool, panes ...int) (*ProbeSessionOutput, int) {
	return GetProbeSession(ProbeSessionOptions{Session: "session", Panes: panes, Flags: ProbeFlags{Method: method, TimeoutMs: 1, Aggressive: aggressive}})
}
func assertNoProbeInput(t *testing.T, calls []string) {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, "input:") {
			t.Fatalf("unexpected input: %v", calls)
		}
	}
}

func TestProbeExecutionRequiresCompleteUnambiguousSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		panes     []tmux.Pane
		selectors []int
	}{
		{"missing selector", []tmux.Pane{executionPane("%9", 0)}, []int{0, 77}},
		{"duplicate identity", []tmux.Pane{executionPane("%9", 0), executionPane("%9", 1)}, []int{0}},
		{"missing identity", []tmux.Pane{executionPane("", 0)}, []int{0}},
		{"invalid identity", []tmux.Pane{executionPane("%1;kill-server", 0)}, []int{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newProbeExecutionClient(t, tc.panes...)
			out, exit := runExecutionProbe(ProbeMethodKeystrokeEcho, false, tc.selectors...)
			if exit == 0 || out.Success || out.ErrorCode != ErrCodePaneNotFound || len(out.Probes) != 0 {
				t.Fatalf("invalid selection accepted: %+v exit=%d", out, exit)
			}
			assertNoProbeInput(t, client.calls)
		})
	}
}

func TestProbeExecutionPinsInputAndCleanupAcrossRenumbering(t *testing.T) {
	first, second := executionPane("%9", 0), executionPane("%10", 1)
	client := newProbeExecutionClient(t, first, second)
	client.discover = func(n int) ([]tmux.Pane, error) {
		if n > 1 {
			first.Index, second.Index = 1, 0
		}
		return []tmux.Pane{first, second}, nil
	}
	out, exit := runExecutionProbe(ProbeMethodKeystrokeEcho, false, 0, 1)
	if exit != 0 || !out.Success || out.Summary.Responsive != 2 {
		t.Fatalf("renumbered probe: %+v exit=%d", out, exit)
	}
	want := []string{"capture:%9", "input:%9: ", "capture:%9", "input:%9:BSpace", "capture:%10", "input:%10: ", "capture:%10", "input:%10:BSpace"}
	if !reflect.DeepEqual(client.calls, want) {
		t.Fatalf("target changed: %v want %v", client.calls, want)
	}
	for i, id := range []string{"%9", "%10"} {
		if out.Probes[i].PaneID != id || !out.Probes[i].ProbeDetails.CleanupSucceeded {
			t.Fatalf("missing receipt: %+v", out.Probes[i])
		}
	}
}

func TestProbeExecutionRejectsReplacedRetaggedOrMovedTargets(t *testing.T) {
	for _, change := range []string{"missing", "replacement", "pid", "command", "type", "dead", "service", "duplicate"} {
		t.Run(change, func(t *testing.T) {
			original := executionPane("%9", 0)
			client := newProbeExecutionClient(t, original)
			client.discover = func(n int) ([]tmux.Pane, error) {
				if n == 1 {
					return []tmux.Pane{original}, nil
				}
				current := original
				switch change {
				case "missing":
					return nil, nil
				case "replacement":
					current.ID = "%99"
				case "pid":
					current.PID++
				case "command":
					current.Command = "bash"
				case "type":
					current.Type = tmux.AgentCodex
				case "dead":
					current.Dead = true
				case "service":
					current.Service = "cass"
				case "duplicate":
					return []tmux.Pane{current, current}, nil
				}
				return []tmux.Pane{current}, nil
			}
			out, exit := runExecutionProbe(ProbeMethodKeystrokeEcho, true, 0)
			if exit == 0 || out.Summary.Errors != 1 || out.Probes[0].Recommendation != ProbeRecommendationUnknown {
				t.Fatalf("changed identity accepted: %+v", out)
			}
			assertNoProbeInput(t, client.calls)
		})
	}
}

func TestProbeExecutionDoesNotCleanupIntoReplacementProcess(t *testing.T) {
	pane := executionPane("%9", 0)
	client := newProbeExecutionClient(t, pane)
	client.send = func(_, text string) error {
		if text == " " {
			client.panes[0].PID++
		}
		return nil
	}
	out, exit := runExecutionProbe(ProbeMethodKeystrokeEcho, true, 0)
	if exit == 0 || out.Summary.Errors != 1 || out.Probes[0].ProbeDetails.CleanupAttempted {
		t.Fatalf("unsafe cleanup: %+v", out)
	}
	if strings.Contains(strings.Join(client.calls, "|"), "BSpace") || strings.Contains(strings.Join(client.calls, "|"), "C-c") {
		t.Fatalf("replacement received input: %v", client.calls)
	}
}

func TestProbeExecutionTransportFailuresNeverEscalate(t *testing.T) {
	for _, stage := range []string{"baseline", "stimulus", "response", "cleanup", "tail"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("transport unavailable")
			client := newProbeExecutionClient(t, executionPane("%9", 0))
			client.capture = func(_ string, n int) (string, error) {
				if (stage == "baseline" && n == 1) || (stage == "response" && n == 2) || (stage == "tail" && n == 3) {
					return "", failure
				}
				return fmt.Sprintf("frame %d", n), nil
			}
			client.send = func(_, text string) error {
				if (stage == "stimulus" && text == " ") || (stage == "cleanup" && text == "BSpace") {
					return failure
				}
				return nil
			}
			method, aggressive := ProbeMethodKeystrokeEcho, true
			if stage == "tail" {
				method, aggressive = ProbeMethodWakePing, false
			}
			out, exit := runExecutionProbe(method, aggressive, 0)
			if exit == 0 || out.Success || out.Summary.Errors != 1 || out.Summary.Unresponsive != 0 || out.Probes[0].ErrorCode != ErrCodeInternalError || out.Probes[0].Recommendation != ProbeRecommendationUnknown {
				t.Fatalf("operational failure became hang: %+v exit=%d", out, exit)
			}
			if strings.Contains(strings.Join(client.calls, "|"), "C-c") {
				t.Fatalf("failure escalated: %v", client.calls)
			}
			if stage == "baseline" {
				assertNoProbeInput(t, client.calls)
			}
			if stage == "stimulus" && out.Probes[0].ProbeDetails.CleanupAttempted {
				t.Fatal("uncertain stimulus received blind cleanup")
			}
			if stage == "cleanup" && (!out.Probes[0].Responsive || out.Probes[0].ProbeDetails.CleanupSucceeded) {
				t.Fatalf("lost response or invented cleanup: %+v", out.Probes[0])
			}
			if stage == "tail" && out.Probes[0].ProbeDetails.StillRateLimited != nil {
				t.Fatal("failed tail invented a rate-limit verdict")
			}
		})
	}
}

func TestProbeExecutionAggressiveTimeoutReportsActualMethod(t *testing.T) {
	client := newProbeExecutionClient(t, executionPane("%9", 0))
	interrupted := false
	client.send = func(_, text string) error { interrupted = interrupted || text == "C-c"; return nil }
	client.capture = func(_ string, _ int) (string, error) {
		if interrupted {
			return "interrupted", nil
		}
		return "unchanged", nil
	}
	out, exit := runExecutionProbe(ProbeMethodKeystrokeEcho, true, 0)
	if exit != 0 || out.Probes[0].ProbeMethod != ProbeMethodInterruptTest || out.Probes[0].ProbeDetails.InputSent != "Ctrl-C" {
		t.Fatalf("escalation receipt: %+v exit=%d", out, exit)
	}
}

func TestProbeExecutionPreflightsWakeAndUnsafePanes(t *testing.T) {
	for _, kind := range []string{"shell", "dead", "service"} {
		t.Run(kind, func(t *testing.T) {
			pane := executionPane("%10", 1)
			switch kind {
			case "shell":
				pane.Type = tmux.AgentUser
			case "dead":
				pane.Dead = true
			case "service":
				pane.Service = "cm"
			}
			client := newProbeExecutionClient(t, executionPane("%9", 0), pane)
			out, exit := runExecutionProbe(ProbeMethodWakePing, false, 0, 1)
			if exit == 0 || out.Success || len(out.Probes) != 0 {
				t.Fatalf("mixed unsafe batch accepted: %+v", out)
			}
			assertNoProbeInput(t, client.calls)
		})
	}
}

func TestProbeExecutionPreservesIndependentResults(t *testing.T) {
	client := newProbeExecutionClient(t, executionPane("%9", 0), executionPane("%10", 1))
	client.capture = func(target string, n int) (string, error) {
		if target == "%10" {
			return "", errors.New("lost capture")
		}
		return fmt.Sprint(n), nil
	}
	out, exit := runExecutionProbe(ProbeMethodKeystrokeEcho, false, 0, 1)
	if exit != 1 || out.Summary.TotalProbed != 2 || out.Summary.Responsive != 1 || out.Summary.Errors != 1 || !out.Probes[0].Responsive || out.Probes[1].Error == "" {
		t.Fatalf("partial result: %+v exit=%d", out, exit)
	}
}

func TestProbeExecutionPreservesFailureCause(t *testing.T) {
	failure := errors.New("capture failure")
	client := newProbeExecutionClient(t, executionPane("%9", 0))
	client.capture = func(string, int) (string, error) { return "", failure }
	result := probeInterruptTest("%9", tmux.AgentClaude, time.Millisecond)
	if !errors.Is(result.Err, failure) || result.Details.InputAttempted || result.Details.InputSent != "" {
		t.Fatalf("cause or input receipt: %+v", result)
	}
	assertNoProbeInput(t, client.calls)
}

// ntm#329: --robot-probe must accept the same N, W.P, and %N selectors as every
// other robot --panes flag, and an explicit W.P or %N must reach exactly the
// named pane rather than the first pane of a same-numbered window.
func TestProbeExecutionAcceptsSharedPaneSelectorGrammar(t *testing.T) {
	windowPane := func(id string, window, index int) tmux.Pane {
		pane := executionPane(id, index)
		pane.WindowIndex = window
		return pane
	}
	topology := []tmux.Pane{
		windowPane("%1", 1, 1),
		windowPane("%2", 1, 2),
		windowPane("%3", 2, 1),
	}
	probeInputs := func(calls []string) []string {
		var targets []string
		for _, call := range calls {
			if strings.HasPrefix(call, "input:") && strings.HasSuffix(call, ": ") {
				targets = append(targets, strings.TrimSuffix(strings.TrimPrefix(call, "input:"), ": "))
			}
		}
		return targets
	}

	for _, tc := range []struct {
		name      string
		selectors []string
		wantIDs   []string
		wantRefs  []string
		wantSel   []string
		wantPane  []int
	}{
		{"window.pane names one pane in a split window", []string{"1.2"}, []string{"%2"}, []string{"1.2"}, []string{"1.2"}, []int{2}},
		{"pane id names one pane", []string{"%3"}, []string{"%3"}, []string{"2.1"}, []string{"%3"}, []int{1}},
		{"bare N keeps the shared window meaning", []string{"1"}, []string{"%1", "%2"}, []string{"1.1", "1.2"}, []string{"1", "1"}, []int{1, 1}},
		{"aliases of one pane probe it once", []string{"%2", "1.2"}, []string{"%2"}, []string{"1.2"}, []string{"%2"}, []int{2}},
		{"mixed selectors", []string{"2", "%1"}, []string{"%1", "%3"}, []string{"1.1", "2.1"}, []string{"%1", "2"}, []int{1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newProbeExecutionClient(t, topology...)
			out, exit := GetProbeSession(ProbeSessionOptions{
				Session:       "session",
				PaneSelectors: tc.selectors,
				Flags:         ProbeFlags{Method: ProbeMethodKeystrokeEcho, TimeoutMs: 1},
			})
			if exit != 0 || !out.Success {
				t.Fatalf("probe %v failed: %+v exit=%d", tc.selectors, out.RobotResponse, exit)
			}
			if got := probeInputs(client.calls); !reflect.DeepEqual(got, tc.wantIDs) {
				t.Fatalf("probe input reached %v, want %v (calls=%v)", got, tc.wantIDs, client.calls)
			}
			if len(out.Probes) != len(tc.wantRefs) {
				t.Fatalf("got %d probe entries, want %d: %+v", len(out.Probes), len(tc.wantRefs), out.Probes)
			}
			for i, probe := range out.Probes {
				if probe.PaneID != tc.wantIDs[i] || probe.PaneRef != tc.wantRefs[i] || probe.Selector != tc.wantSel[i] || probe.Pane != tc.wantPane[i] {
					t.Fatalf("entry %d = {id:%s ref:%s selector:%q pane:%d}, want {id:%s ref:%s selector:%q pane:%d}",
						i, probe.PaneID, probe.PaneRef, probe.Selector, probe.Pane, tc.wantIDs[i], tc.wantRefs[i], tc.wantSel[i], tc.wantPane[i])
				}
			}
		})
	}

	for _, tc := range []struct {
		name      string
		selectors []string
		wantCode  string
	}{
		{"unknown window.pane", []string{"1.1", "1.9"}, ErrCodePaneNotFound},
		{"unknown pane id", []string{"%99"}, ErrCodePaneNotFound},
		{"malformed selector", []string{"1.x"}, ErrCodeInvalidFlag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newProbeExecutionClient(t, topology...)
			out, exit := GetProbeSession(ProbeSessionOptions{
				Session:       "session",
				PaneSelectors: tc.selectors,
				Flags:         ProbeFlags{Method: ProbeMethodKeystrokeEcho, TimeoutMs: 1},
			})
			if exit == 0 || out.Success || out.ErrorCode != tc.wantCode || len(out.Probes) != 0 {
				t.Fatalf("selectors %v: %+v exit=%d, want %s with no probes", tc.selectors, out.RobotResponse, exit, tc.wantCode)
			}
			assertNoProbeInput(t, client.calls)
		})
	}
}
