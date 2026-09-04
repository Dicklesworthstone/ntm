package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestStateIcon(t *testing.T) {
	tests := []struct {
		state string
		want  string
	}{
		{"WAITING", "●"},
		{"GENERATING", "▶"},
		{"THINKING", "◐"},
		{"ERROR", "✗"},
		{"STALLED", "◯"},
		{"unknown", "?"},
		{"", "?"},
		{"waiting", "?"}, // case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			got := stateIcon(tt.state)
			if got != tt.want {
				t.Errorf("stateIcon(%q) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestFormatActivityDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"zero", 0, "-"},
		{"1 second", 1 * time.Second, "1s"},
		{"30 seconds", 30 * time.Second, "30s"},
		{"59 seconds", 59 * time.Second, "59s"},
		{"1 minute", 1 * time.Minute, "1m0s"},
		{"1 minute 30 seconds", 90 * time.Second, "1m30s"},
		{"5 minutes", 5 * time.Minute, "5m0s"},
		{"5 minutes 45 seconds", 5*time.Minute + 45*time.Second, "5m45s"},
		{"59 minutes 59 seconds", 59*time.Minute + 59*time.Second, "59m59s"},
		{"1 hour", 1 * time.Hour, "1h0m"},
		{"1 hour 30 minutes", 90 * time.Minute, "1h30m"},
		{"2 hours 15 minutes", 2*time.Hour + 15*time.Minute, "2h15m"},
		{"24 hours", 24 * time.Hour, "24h0m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatActivityDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatActivityDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestPassesFilter(t *testing.T) {

	tests := []struct {
		name      string
		agentType string
		pane      tmux.Pane
		opts      activityOptions
		want      bool
	}{
		{
			name:      "no_filters_allows_all",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{},
			want:      true,
		},
		{
			name:      "filter_by_pane_title_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterPane: "cc_1"},
			want:      true,
		},
		{
			name:      "filter_by_pane_title_no_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterPane: "cc_2"},
			want:      false,
		},
		{
			name:      "filter_by_pane_index_match",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterPane: "2"},
			want:      true,
		},
		{
			name:      "filter_by_pane_index_no_match",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterPane: "3"},
			want:      false,
		},
		{
			name:      "filter_claude_type_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterClaude: true},
			want:      true,
		},
		{
			name:      "filter_claude_type_no_match",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterClaude: true},
			want:      false,
		},
		{
			name:      "filter_codex_type_match",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterCodex: true},
			want:      true,
		},
		{
			name:      "filter_codex_type_no_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterCodex: true},
			want:      false,
		},
		{
			name:      "filter_gemini_type_match",
			agentType: "gemini",
			pane:      tmux.Pane{Index: 3, Title: "gmi_3"},
			opts:      activityOptions{filterGemini: true},
			want:      true,
		},
		{
			name:      "filter_gemini_type_no_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterGemini: true},
			want:      false,
		},
		{
			name:      "filter_grok_type_match",
			agentType: "grok",
			pane:      tmux.Pane{Index: 4, Title: "grok_4"},
			opts:      activityOptions{filterGrok: true},
			want:      true,
		},
		{
			name:      "filter_grok_type_no_match",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterGrok: true},
			want:      false,
		},
		{
			name:      "multiple_type_filters_match_first",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterClaude: true, filterCodex: true},
			want:      true,
		},
		{
			name:      "multiple_type_filters_match_second",
			agentType: "codex",
			pane:      tmux.Pane{Index: 2, Title: "cod_2"},
			opts:      activityOptions{filterClaude: true, filterCodex: true},
			want:      true,
		},
		{
			name:      "multiple_type_filters_no_match",
			agentType: "gemini",
			pane:      tmux.Pane{Index: 3, Title: "gmi_3"},
			opts:      activityOptions{filterClaude: true, filterCodex: true},
			want:      false,
		},
		{
			name:      "all_type_filters_match_all",
			agentType: "gemini",
			pane:      tmux.Pane{Index: 3, Title: "gmi_3"},
			opts:      activityOptions{filterClaude: true, filterCodex: true, filterGemini: true},
			want:      true,
		},
		{
			name:      "pane_filter_takes_precedence_over_type",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterPane: "cc_1", filterCodex: true},
			want:      true, // pane filter matches, type filter is ignored
		},
		{
			name:      "pane_filter_precedence_no_match",
			agentType: "claude",
			pane:      tmux.Pane{Index: 1, Title: "cc_1"},
			opts:      activityOptions{filterPane: "cc_99", filterClaude: true},
			want:      false, // pane filter doesn't match, type filter is ignored
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := passesFilter(tt.agentType, tt.pane, tt.opts, false)
			if got != tt.want {
				t.Errorf("passesFilter(%q, %v, %+v) = %v, want %v",
					tt.agentType, tt.pane, tt.opts, got, tt.want)
			}
		})
	}
}

func TestDetectAgentTypeFromPane_GrokAlias(t *testing.T) {
	pane := tmux.Pane{Type: tmux.AgentType("xai-grok-build")}
	if got := detectAgentTypeFromPane(pane); got != "grok" {
		t.Fatalf("detectAgentTypeFromPane(%q) = %q, want grok", pane.Type, got)
	}
}

func TestActivityAgentTypeColor(t *testing.T) {

	current := theme.Current()
	tests := []struct {
		name      string
		agentType string
		want      string
	}{
		{"claude", "claude", string(current.Claude)},
		{"codex alias", "openai-codex", string(current.Codex)},
		{"gemini alias", "google-gemini", string(current.Gemini)},
		{"grok alias", "xai-grok-build", string(current.Pink)},
		{"cursor", "cursor", string(current.Cursor)},
		{"windsurf alias", "ws", string(current.Windsurf)},
		{"aider", "aider", string(current.Aider)},
		{"opencode short", "oc", string(current.Opencode)},
		{"opencode long", "opencode", string(current.Opencode)},
		{"ollama", "ollama", string(current.Ollama)},
		{"user", "user", string(current.User)},
		{"unknown", "mystery", string(current.Text)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(activityAgentTypeColor(tc.agentType, current)); got != tc.want {
				t.Fatalf("activityAgentTypeColor(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestActivityCommandAdvertisesGrokFilter(t *testing.T) {
	flag := newActivityCmd().Flags().Lookup("grok")
	if flag == nil {
		t.Fatal("activity command omits --grok filter")
	}
	if !strings.Contains(flag.Usage, "Grok Build") {
		t.Fatalf("--grok usage = %q, want Grok Build", flag.Usage)
	}
}

// TestOutputActivityError_ReturnsJSONFailureSentinel covers bd-usgfy: after
// outputActivityError successfully encodes a `success: false` envelope, it
// must return errJSONFailure so root.Execute() exits non-zero. Without this,
// `ntm activity --json` automation that gates on `$?` silently misses
// outages.
func TestOutputActivityError_ReturnsJSONFailureSentinel(t *testing.T) {
	t.Helper()

	// Redirect stdout so the encoded envelope doesn't pollute test output.
	origStdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe error = %v", pipeErr)
	}
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()

	err := outputActivityError("test-session", fmt.Errorf("synthetic activity failure"))
	_ = w.Close()
	<-done

	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("outputActivityError returned %v, want errJSONFailure", err)
	}
}

// TestRunActivity_EarlyFailRoutesThroughJSONEnvelope covers bd-ixy2t: when
// --json is set, runActivity's early-fail paths (tmux.EnsureInstalled,
// ResolveSession) must emit a parseable failure envelope instead of
// returning the raw error. Pre-fix, automation pipelines like
// `ntm activity --json | jq -r .session` got a stderr "Error:" line and
// empty stdin to jq.
//
// We exercise the ResolveSession failure site (it can be triggered
// deterministically by passing an invalid session name) and assert
// runActivity returns errJSONFailure — proving the fix routed the early
// error through outputActivityError instead of returning it raw.
func TestRunActivity_EarlyFailRoutesThroughJSONEnvelope(t *testing.T) {
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	origStdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe error = %v", pipeErr)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()

	// Invalid session name (spaces) trips tmux.ValidateSessionName inside
	// ResolveSession, which is one of the bd-ixy2t early-fail sites.
	err := runActivity("not a valid name", activityOptions{})
	_ = w.Close()
	<-done

	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runActivity returned %v, want errJSONFailure (early-fail must route through outputActivityError)", err)
	}
}

// TestRunAdopt_EarlyFailRoutesThroughJSONEnvelope covers bd-ixy2t for
// the adopt path: the tmux.EnsureInstalled failure site (only deterministic
// early-fail in runAdopt without injecting a tmux failure) must route
// through emitAdoptFailure when --json is set. We can't make a real
// tmux disappear in-process, but the symmetric SessionExists failure
// already proves the JSON envelope helper fires; this test locks the
// jsonOutput contract specifically for the SessionExists branch with a
// session name guaranteed not to exist, so the closure runs and the
// errJSONFailure sentinel propagates up.
func TestRunAdopt_SessionMissingRoutesThroughJSONEnvelope(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed; the SessionExists path requires a working tmux client")
	}
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	origStdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe error = %v", pipeErr)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()

	// Session that doesn't exist trips the SessionExists branch — the
	// emitAdoptFailure closure now lives above the tmux.EnsureInstalled
	// gate so the routing contract holds for both early-fail sites.
	err := runAdopt(AdoptOptions{Session: "ntm-bd-ixy2t-nonexistent-session"})
	_ = w.Close()
	<-done

	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runAdopt returned %v, want errJSONFailure", err)
	}
}

// TestActivity_TwoWindowPaneIndexCollision covers the topology where the
// per-window pane index collides across windows: a session with two windows
// whose only panes are each "pane 1" of their own window. `ntm activity` used
// to print two indistinguishable rows for that layout; window_index and the
// stable tmux pane_id must now disambiguate them in both the collected data
// and the --json envelope, while the legacy "pane" field stays intact for
// existing consumers.
func TestActivity_TwoWindowPaneIndexCollision(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	workDir := t.TempDir()
	session := fmt.Sprintf("ntm-test-activity-win-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, workDir); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	first, err := tmux.GetPanes(session)
	if err != nil || len(first) != 1 {
		t.Fatalf("initial panes = %d (err=%v), want 1", len(first), err)
	}
	if _, err := tmux.DefaultClient.Run("select-pane", "-t", first[0].ID, "-T", session+"__cc_1"); err != nil {
		t.Fatalf("title first pane: %v", err)
	}
	if _, err := tmux.DefaultClient.Run("new-window", "-d", "-t", session, "-c", workDir); err != nil {
		t.Fatalf("new-window: %v", err)
	}
	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) != 2 {
		t.Fatalf("panes after new-window = %d (err=%v), want 2", len(panes), err)
	}
	for _, p := range panes {
		if p.WindowIndex != first[0].WindowIndex {
			if _, err := tmux.DefaultClient.Run("select-pane", "-t", p.ID, "-T", session+"__cc_2"); err != nil {
				t.Fatalf("title second-window pane: %v", err)
			}
		}
	}

	// Precondition: this IS the collision — same per-window index, different
	// windows, distinct stable IDs.
	panes, err = tmux.GetPanes(session)
	if err != nil || len(panes) != 2 {
		t.Fatalf("re-read panes = %d (err=%v), want 2", len(panes), err)
	}
	if panes[0].Index != panes[1].Index {
		t.Fatalf("precondition: pane indexes differ (%d vs %d); collision not reproduced", panes[0].Index, panes[1].Index)
	}
	if panes[0].WindowIndex == panes[1].WindowIndex || panes[0].ID == panes[1].ID || panes[0].ID == "" {
		t.Fatalf("precondition: panes not in distinct windows with distinct IDs: %+v", panes)
	}

	result, err := collectActivityData(session, activityOptions{})
	if err != nil {
		t.Fatalf("collectActivityData: %v", err)
	}
	if len(result.Agents) != 2 {
		t.Fatalf("agents = %d, want 2: %+v", len(result.Agents), result.Agents)
	}

	envelope := buildActivityJSONEnvelope(result)
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	for _, field := range []string{`"pane"`, `"window_index"`, `"pane_id"`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("envelope missing %s field: %s", field, data)
		}
	}

	seenIDs := map[string]bool{}
	seenWindows := map[int]bool{}
	for _, a := range envelope.Agents {
		if a.PaneID == "" {
			t.Fatalf("agent row missing pane_id: %+v", a)
		}
		if seenIDs[a.PaneID] {
			t.Fatalf("pane_id %s repeated — rows are still indistinguishable: %+v", a.PaneID, envelope.Agents)
		}
		seenIDs[a.PaneID] = true
		seenWindows[a.WindowIndex] = true
	}
	if len(seenWindows) != 2 {
		t.Fatalf("window_index did not separate the colliding rows: %+v", envelope.Agents)
	}
}

// TestActivityStateScopeSeparatesPaneLifetimes pins the scope key used for the
// durable state-age watermark (ntm#301): tmux recycles pane IDs, so a scope
// that ignored the pane PID would hand a fresh process the previous one's age.
func TestActivityStateScopeSeparatesPaneLifetimes(t *testing.T) {
	base := activityStateScope("agent-factory", tmux.Pane{ID: "%9", PID: 4242})
	newProcess := activityStateScope("agent-factory", tmux.Pane{ID: "%9", PID: 5252})
	otherSession := activityStateScope("workshop", tmux.Pane{ID: "%9", PID: 4242})
	otherPane := activityStateScope("agent-factory", tmux.Pane{ID: "%10", PID: 4242})

	for _, other := range []string{newProcess, otherSession, otherPane} {
		if base == other {
			t.Fatalf("activity state scope aliases distinct pane lifetimes: %q == %q", base, other)
		}
	}
}
