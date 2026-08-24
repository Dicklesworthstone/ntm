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

// TestActivityEmptyMessage covers bd-3mz5g: when no agents are rendered, the
// message must distinguish an empty session (no panes) from a session whose
// panes all failed classification, and report unknown vs user panes
// separately. The unclassified message names what to check.
func TestActivityEmptyMessage(t *testing.T) {
	const checkHint = "Check the pane title, the pane command, and whether the agent process sits deeper than the four levels detectAgentFromProcessTree walks."

	tests := []struct {
		name   string
		result *activityResult
		want   string
	}{
		{
			name:   "zero_panes",
			result: &activityResult{PaneCount: 0, State: activityStateEmpty},
			want:   "No panes found in session",
		},
		{
			name: "all_unknown",
			result: &activityResult{
				PaneCount:    4,
				UnknownCount: 4,
				State:        activityStateUnclassified,
			},
			want: "4 panes, none classified as agents\n" +
				"  4 unclassified (unknown)\n" +
				checkHint,
		},
		{
			name: "all_user",
			result: &activityResult{
				PaneCount: 2,
				UserCount: 2,
				State:     activityStateUnclassified,
			},
			want: "2 panes, none classified as agents\n" +
				"  2 user panes (expected)\n" +
				checkHint,
		},
		{
			name: "mixed_unknown_and_user",
			result: &activityResult{
				PaneCount:    6,
				UnknownCount: 4,
				UserCount:    2,
				State:        activityStateUnclassified,
			},
			want: "6 panes, none classified as agents\n" +
				"  4 unclassified (unknown)\n" +
				"  2 user panes (expected)\n" +
				checkHint,
		},
		{
			name: "filtered_out_keeps_historical_message",
			result: &activityResult{
				PaneCount: 2,
				State:     activityStateOK,
			},
			want: "No agents found in session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activityEmptyMessage(tt.result); got != tt.want {
				t.Errorf("activityEmptyMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestActivitySkippedMessage covers bd-3mz5g: when at least one agent is
// rendered, skipped panes are reported with unknown and user counted
// separately.
func TestActivitySkippedMessage(t *testing.T) {
	tests := []struct {
		name   string
		result *activityResult
		want   string
	}{
		{name: "no_skips", result: &activityResult{}, want: ""},
		{
			name:   "unknown_only",
			result: &activityResult{UnknownCount: 2},
			want:   "Skipped: 2 unclassified (unknown)",
		},
		{
			name:   "user_only",
			result: &activityResult{UserCount: 1},
			want:   "Skipped: 1 user pane",
		},
		{
			name:   "mixed",
			result: &activityResult{UnknownCount: 2, UserCount: 1},
			want:   "Skipped: 2 unclassified (unknown), 1 user pane",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activitySkippedMessage(tt.result); got != tt.want {
				t.Errorf("activitySkippedMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBuildActivityResult covers bd-3mz5g over a stubbed pane list (no tmux
// server): the result carries pane/unknown/user counts and a state field that
// separates an empty session from an unclassified one, while a classified
// agent is still rendered.
func TestBuildActivityResult(t *testing.T) {
	unknownPane := tmux.Pane{Index: 1, Title: "zsh", Type: tmux.AgentUnknown}
	userPane := tmux.Pane{Index: 2, Title: "user", Type: tmux.AgentUser}
	claudePane := tmux.Pane{ID: "%999999", Index: 3, Title: "cc_3", Type: tmux.AgentClaude}

	tests := []struct {
		name        string
		panes       []tmux.Pane
		wantState   string
		wantPane    int
		wantUnknown int
		wantUser    int
		wantAgents  int
	}{
		{
			name:      "zero_panes",
			panes:     nil,
			wantState: activityStateEmpty,
		},
		{
			name:        "all_unknown",
			panes:       []tmux.Pane{unknownPane, unknownPane},
			wantState:   activityStateUnclassified,
			wantPane:    2,
			wantUnknown: 2,
		},
		{
			name:      "all_user",
			panes:     []tmux.Pane{userPane},
			wantState: activityStateUnclassified,
			wantPane:  1,
			wantUser:  1,
		},
		{
			name:        "mix_user_unknown_agent",
			panes:       []tmux.Pane{userPane, unknownPane, claudePane},
			wantState:   activityStateOK,
			wantPane:    3,
			wantUnknown: 1,
			wantUser:    1,
			wantAgents:  1,
		},
		{
			name:       "one_agent_only",
			panes:      []tmux.Pane{claudePane},
			wantState:  activityStateOK,
			wantPane:   1,
			wantAgents: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildActivityResult("test-session", tt.panes, activityOptions{})
			if result.State != tt.wantState {
				t.Errorf("State = %q, want %q", result.State, tt.wantState)
			}
			if result.PaneCount != tt.wantPane {
				t.Errorf("PaneCount = %d, want %d", result.PaneCount, tt.wantPane)
			}
			if result.UnknownCount != tt.wantUnknown {
				t.Errorf("UnknownCount = %d, want %d", result.UnknownCount, tt.wantUnknown)
			}
			if result.UserCount != tt.wantUser {
				t.Errorf("UserCount = %d, want %d", result.UserCount, tt.wantUser)
			}
			if len(result.Agents) != tt.wantAgents {
				t.Errorf("len(Agents) = %d, want %d", len(result.Agents), tt.wantAgents)
			}
		})
	}
}

// TestActivityJSONStateField covers bd-3mz5g: the --json envelope carries the
// empty/unclassified/ok distinction as a field, asserted by round-trip, so a
// caller can tell the two states apart without parsing prose.
func TestActivityJSONStateField(t *testing.T) {
	tests := []struct {
		name        string
		panes       []tmux.Pane
		wantState   string
		wantPane    int
		wantUnknown int
		wantUser    int
	}{
		{
			name:      "empty",
			panes:     nil,
			wantState: activityStateEmpty,
		},
		{
			name:        "unclassified",
			panes:       []tmux.Pane{{Index: 1, Title: "zsh", Type: tmux.AgentUnknown}},
			wantState:   activityStateUnclassified,
			wantPane:    1,
			wantUnknown: 1,
		},
		{
			name:      "user",
			panes:     []tmux.Pane{{Index: 1, Title: "user", Type: tmux.AgentUser}},
			wantState: activityStateUnclassified,
			wantPane:  1,
			wantUser:  1,
		},
		{
			name:      "ok",
			panes:     []tmux.Pane{{ID: "%999999", Index: 1, Title: "cc_1", Type: tmux.AgentClaude}},
			wantState: activityStateOK,
			wantPane:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildActivityResult("json-session", tt.panes, activityOptions{})
			env := buildActivityJSONEnvelope(result)

			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var roundTripped activityJSONEnvelope
			if err := json.Unmarshal(raw, &roundTripped); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if roundTripped.State != tt.wantState {
				t.Errorf("state = %q, want %q", roundTripped.State, tt.wantState)
			}
			if roundTripped.PaneCount != tt.wantPane {
				t.Errorf("pane_count = %d, want %d", roundTripped.PaneCount, tt.wantPane)
			}
			if roundTripped.UnknownCount != tt.wantUnknown {
				t.Errorf("unknown_count = %d, want %d", roundTripped.UnknownCount, tt.wantUnknown)
			}
			if roundTripped.UserCount != tt.wantUser {
				t.Errorf("user_count = %d, want %d", roundTripped.UserCount, tt.wantUser)
			}
		})
	}
}
