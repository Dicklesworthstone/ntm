package robot

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestDefaultIsWorkingOptions(t *testing.T) {
	opts := DefaultIsWorkingOptions()

	if opts.LinesCaptured != 100 {
		t.Errorf("expected LinesCaptured=100, got %d", opts.LinesCaptured)
	}
	if opts.Verbose {
		t.Error("expected Verbose=false")
	}
	if opts.Session != "" {
		t.Errorf("expected empty Session, got %q", opts.Session)
	}
	if len(opts.Panes) != 0 {
		t.Errorf("expected empty Panes, got %v", opts.Panes)
	}
}

func TestPrintIsWorkingFailureReturnsTypedErrorAndRawJSON(t *testing.T) {
	originalFormat := GetOutputFormat()
	SetOutputFormat(FormatTOON)
	t.Cleanup(func() { SetOutputFormat(originalFormat) })

	stdout, err := captureStdout(t, func() error {
		return PrintIsWorking(t.Context(), IsWorkingOptions{Session: "ntm-is-working-missing-session-for-test"})
	})
	if err == nil {
		t.Fatal("PrintIsWorking() error = nil, want typed terminal failure")
	}
	var response IsWorkingOutput
	if json.Unmarshal([]byte(stdout), &response) != nil {
		t.Fatalf("stdout is not raw JSON: %q", stdout)
	}
	if response.Success || response.ErrorCode != ErrCodeSessionNotFound || response.OutputFormat != string(FormatJSON) {
		t.Fatalf("response = %+v, want SESSION_NOT_FOUND JSON failure", response)
	}
	if response.Query.PanesRequested == nil || response.Panes == nil || response.Summary.ByRecommendation == nil {
		t.Fatalf("critical collections must be empty, not null: %+v", response)
	}
}

func TestGetIsWorkingCanceledContextReturnsTimeoutWithoutObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := GetIsWorking(ctx, IsWorkingOptions{Session: "must-not-observe"})
	if err != nil {
		t.Fatalf("GetIsWorking() error=%v", err)
	}
	if out == nil || out.Success || out.ErrorCode != ErrCodeTimeout || !strings.Contains(strings.ToLower(out.Error), "canceled") {
		t.Fatalf("canceled is-working output=%+v", out)
	}
}

func TestParsePanesArg(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  []int
		expectErr bool
	}{
		{
			name:     "empty string returns empty slice",
			input:    "",
			expected: []int{},
		},
		{
			name:     "all keyword returns empty slice",
			input:    "all",
			expected: []int{},
		},
		{
			name:     "ALL uppercase returns empty slice",
			input:    "ALL",
			expected: []int{},
		},
		{
			name:     "single pane",
			input:    "2",
			expected: []int{2},
		},
		{
			name:     "multiple panes",
			input:    "1,2,3",
			expected: []int{1, 2, 3},
		},
		{
			name:     "panes with spaces",
			input:    "1, 2, 3",
			expected: []int{1, 2, 3},
		},
		{
			name:     "pane zero is valid",
			input:    "0",
			expected: []int{0},
		},
		{
			name:      "negative pane is invalid",
			input:     "-1",
			expectErr: true,
		},
		{
			name:      "non-numeric is invalid",
			input:     "abc",
			expectErr: true,
		},
		{
			name:      "mixed valid and invalid",
			input:     "1,abc,3",
			expectErr: true,
		},
		{
			name:     "trailing comma",
			input:    "1,2,",
			expected: []int{1, 2},
		},
		{
			name:     "leading comma",
			input:    ",1,2",
			expected: []int{1, 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ParsePanesArg(tc.input)

			if tc.expectErr {
				if err == nil {
					t.Errorf("expected error for input %q, got nil", tc.input)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error for input %q: %v", tc.input, err)
				return
			}

			if len(result) != len(tc.expected) {
				t.Errorf("expected %v, got %v", tc.expected, result)
				return
			}

			for i, v := range tc.expected {
				if result[i] != v {
					t.Errorf("at index %d: expected %d, got %d", i, v, result[i])
				}
			}
		})
	}
}

func TestParsePaneSelectorsArg(t *testing.T) {
	selectors, err := ParsePaneSelectorsArg(" 1,1.0,%7,1 ")
	if err != nil {
		t.Fatalf("ParsePaneSelectorsArg() error = %v", err)
	}
	want := []string{"1", "1.0", "%7"}
	if len(selectors) != len(want) {
		t.Fatalf("selectors = %v, want %v", selectors, want)
	}
	for index := range want {
		if selectors[index] != want[index] {
			t.Fatalf("selectors[%d] = %q, want %q", index, selectors[index], want[index])
		}
	}
	for _, input := range []string{"-1", "1.x", "%x", " ", ",", "1,", ",1", "1,,2"} {
		if _, err := ParsePaneSelectorsArg(input); err == nil {
			t.Errorf("ParsePaneSelectorsArg(%q) expected error", input)
		}
	}

	for _, input := range []string{"", "all", " ALL "} {
		selectors, err := ParsePaneSelectorsArg(input)
		if err != nil {
			t.Errorf("ParsePaneSelectorsArg(%q) error = %v", input, err)
			continue
		}
		if len(selectors) != 0 {
			t.Errorf("ParsePaneSelectorsArg(%q) = %v, want empty default selection", input, selectors)
		}
	}
}

func TestResolveIsWorkingPanesSelectorsDeduplicateAliases(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", WindowIndex: 0, Index: 0, Type: tmux.AgentType("claude")},
		{ID: "%2", WindowIndex: 1, Index: 0, Type: tmux.AgentType("codex")},
	}
	selected, err := resolveIsWorkingPanes("proj", panes, []string{"1", "1.0", "%2"}, nil)
	if err != nil {
		t.Fatalf("resolveIsWorkingPanes() error = %v", err)
	}
	if len(selected) != 1 || selected[0].id != "%2" {
		t.Fatalf("selected = %+v, want one physical pane %%2", selected)
	}

	_, err = resolveIsWorkingPanes("proj", panes, []string{"9.0"}, nil)
	if err == nil || paneSelectorRobotErrorCode(err) != ErrCodePaneNotFound {
		t.Fatalf("missing selector error = %v, code = %q", err, paneSelectorRobotErrorCode(err))
	}
	_, err = resolveIsWorkingPanes("proj", panes, []string{"1.x"}, nil)
	if err == nil || paneSelectorRobotErrorCode(err) != ErrCodeInvalidFlag {
		t.Fatalf("invalid selector error = %v, code = %q", err, paneSelectorRobotErrorCode(err))
	}
}

func TestPaneWorkStatusReportsUnavailableCurrentObservation(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	observation := statuspkg.PaneObservation{
		AgentType: "cod",
		Current: statuspkg.StateObservation{
			Status:     statuspkg.AgentStatus{State: statuspkg.StateUnknown},
			ObservedAt: now,
			Freshness:  statuspkg.FreshnessUnavailable,
			Error:      "capture failed",
		},
	}

	got := paneWorkStatusFromObservation(observation)
	if got.ObservationState != "unknown" || got.ObservationFreshness != "unavailable" {
		t.Fatalf("current observation = %q/%q", got.ObservationState, got.ObservationFreshness)
	}
	if got.SafeToDispatch {
		t.Fatal("unavailable current observation must fail closed")
	}
}

func TestApplyCanonicalWorkSafetyFailsClosed(t *testing.T) {
	working := PaneWorkStatus{IsIdle: true, Recommendation: string(agent.RecommendSafeToRestart)}
	applyCanonicalWorkSafety(&working, statuspkg.PaneObservation{Current: statuspkg.StateObservation{
		Status:    statuspkg.AgentStatus{State: statuspkg.StateWorking},
		Freshness: statuspkg.FreshnessFresh,
	}}, false)
	if !working.IsWorking || working.IsIdle || working.Recommendation != string(agent.RecommendDoNotInterrupt) {
		t.Fatalf("working safety override = %+v", working)
	}

	unknown := PaneWorkStatus{IsIdle: true, Recommendation: string(agent.RecommendSafeToRestart)}
	applyCanonicalWorkSafety(&unknown, statuspkg.PaneObservation{Current: statuspkg.StateObservation{
		Status:    statuspkg.AgentStatus{State: statuspkg.StateUnknown},
		Freshness: statuspkg.FreshnessFresh,
	}}, false)
	if unknown.IsWorking || unknown.IsIdle || unknown.Recommendation != string(agent.RecommendUnknown) {
		t.Fatalf("unknown safety override = %+v", unknown)
	}
}

// TestApplyCanonicalWorkSafetyIdleCorrectsStaleWorking is the #234 guard for the
// idle arm: a canonical idle observation must be able to correct a stale parser
// "working" verdict, but only from an actionable observation and never over a
// live-window override, a rate-limit wall, or an error verdict.
func TestApplyCanonicalWorkSafetyIdleCorrectsStaleWorking(t *testing.T) {
	idleObservation := func() statuspkg.PaneObservation {
		return statuspkg.PaneObservation{Current: statuspkg.StateObservation{
			Status:     statuspkg.AgentStatus{State: statuspkg.StateIdle},
			Freshness:  statuspkg.FreshnessFresh,
			Confidence: 0.95,
		}}
	}

	corrected := PaneWorkStatus{IsWorking: true, Recommendation: string(agent.RecommendDoNotInterrupt)}
	applyCanonicalWorkSafety(&corrected, idleObservation(), false)
	if corrected.IsWorking || !corrected.IsIdle || corrected.Recommendation != string(agent.RecommendSafeToRestart) {
		t.Fatalf("idle safety override = %+v", corrected)
	}

	// Negative case 1: the live-window THINKING override (#133) pins working.
	liveBusy := PaneWorkStatus{IsWorking: true, Recommendation: string(agent.RecommendDoNotInterrupt)}
	applyCanonicalWorkSafety(&liveBusy, idleObservation(), true)
	if !liveBusy.IsWorking || liveBusy.IsIdle || liveBusy.Recommendation != string(agent.RecommendDoNotInterrupt) {
		t.Fatalf("live-busy pane was talked down to idle = %+v", liveBusy)
	}

	// Negative case 2: a weak idle observation is not actionable evidence.
	weak := PaneWorkStatus{IsWorking: true, Recommendation: string(agent.RecommendDoNotInterrupt)}
	weakObservation := idleObservation()
	weakObservation.Current.Confidence = 0.5
	applyCanonicalWorkSafety(&weak, weakObservation, false)
	if !weak.IsWorking || weak.IsIdle {
		t.Fatalf("weak idle evidence flipped the verdict = %+v", weak)
	}

	// Negative case 3: rate-limit and error verdicts keep precedence.
	walled := PaneWorkStatus{IsWorking: true, IsRateLimited: true, Recommendation: string(agent.RecommendRateLimitedWait)}
	applyCanonicalWorkSafety(&walled, idleObservation(), false)
	if walled.Recommendation != string(agent.RecommendRateLimitedWait) || walled.IsIdle {
		t.Fatalf("rate-limited pane was advertised as free = %+v", walled)
	}
	broken := PaneWorkStatus{IsWorking: true, Recommendation: string(agent.RecommendErrorState)}
	applyCanonicalWorkSafety(&broken, idleObservation(), false)
	if broken.Recommendation != string(agent.RecommendErrorState) || broken.IsIdle {
		t.Fatalf("errored pane was advertised as free = %+v", broken)
	}
}

func TestGetRecommendationReason(t *testing.T) {
	tests := []struct {
		name     string
		state    *agent.AgentState
		contains string // substring that should be in the reason
	}{
		{
			name: "working agent",
			state: &agent.AgentState{
				IsWorking: true,
			},
			contains: "actively producing",
		},
		{
			name: "idle agent",
			state: &agent.AgentState{
				IsIdle: true,
			},
			contains: "idle",
		},
		{
			name: "rate limited agent",
			state: &agent.AgentState{
				IsRateLimited: true,
			},
			contains: "rate limit",
		},
		{
			name: "context low with percentage",
			state: &agent.AgentState{
				IsWorking:    true,
				IsContextLow: true,
				ContextRemaining: func() *float64 {
					v := 15.0
					return &v
				}(),
			},
			contains: "15%",
		},
		{
			name: "context low without percentage",
			state: &agent.AgentState{
				IsWorking:    true,
				IsContextLow: true,
			},
			contains: "low context",
		},
		{
			name: "error state",
			state: &agent.AgentState{
				IsInError: true,
			},
			contains: "error",
		},
		{
			name:     "unknown state",
			state:    &agent.AgentState{},
			contains: "Could not determine",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason := getRecommendationReason(tc.state)
			if reason == "" {
				t.Error("expected non-empty reason")
			}
			if !containsSubstring(reason, tc.contains) {
				t.Errorf("reason %q does not contain %q", reason, tc.contains)
			}
		})
	}
}

func TestWorkIndicatorsInitialization(t *testing.T) {
	// Ensure WorkIndicators has proper defaults for JSON marshaling
	indicators := WorkIndicators{}

	// After initialization, Work and Limit should be nil
	// But we need to ensure they're set to empty slices in the code
	if indicators.Work != nil {
		t.Error("expected Work to be nil by default")
	}
	if indicators.Limit != nil {
		t.Error("expected Limit to be nil by default")
	}
}

func TestPaneWorkStatusDefaults(t *testing.T) {
	status := PaneWorkStatus{
		AgentType:      "cc",
		Recommendation: "DO_NOT_INTERRUPT",
		Indicators:     WorkIndicators{Work: []string{}, Limit: []string{}},
	}

	if status.AgentType != "cc" {
		t.Errorf("expected AgentType='cc', got %q", status.AgentType)
	}
	if status.IsWorking {
		t.Error("expected IsWorking=false by default")
	}
	if status.IsIdle {
		t.Error("expected IsIdle=false by default")
	}
	if len(status.Indicators.Work) != 0 {
		t.Errorf("expected empty Work indicators, got %v", status.Indicators.Work)
	}
	if len(status.Indicators.Limit) != 0 {
		t.Errorf("expected empty Limit indicators, got %v", status.Indicators.Limit)
	}
}

func TestIsWorkingSummaryInitialization(t *testing.T) {
	summary := IsWorkingSummary{
		ByRecommendation: make(map[string][]string),
	}

	if summary.TotalPanes != 0 {
		t.Errorf("expected TotalPanes=0, got %d", summary.TotalPanes)
	}
	if summary.WorkingCount != 0 {
		t.Errorf("expected WorkingCount=0, got %d", summary.WorkingCount)
	}
	if summary.ByRecommendation == nil {
		t.Error("ByRecommendation should not be nil")
	}
}

func TestIsWorkingQueryFields(t *testing.T) {
	query := IsWorkingQuery{
		PanesRequested: []string{"0.1", "1.0", "%7"},
		LinesCaptured:  100,
	}

	if len(query.PanesRequested) != 3 {
		t.Errorf("expected 3 panes, got %d", len(query.PanesRequested))
	}
	if query.LinesCaptured != 100 {
		t.Errorf("expected LinesCaptured=100, got %d", query.LinesCaptured)
	}
}

func TestIsWorkingOutputStructure(t *testing.T) {
	output := IsWorkingOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test-session",
		Query: IsWorkingQuery{
			PanesRequested: []string{"0.1", "1.0"},
			LinesCaptured:  50,
		},
		Panes: make(map[string]PaneWorkStatus),
		Summary: IsWorkingSummary{
			TotalPanes:       2,
			WorkingCount:     1,
			IdleCount:        1,
			ByRecommendation: map[string][]string{"DO_NOT_INTERRUPT": {"0.1"}, "SAFE_TO_RESTART": {"1.0"}},
		},
	}

	if !output.Success {
		t.Error("expected Success=true")
	}
	if output.Session != "test-session" {
		t.Errorf("expected Session='test-session', got %q", output.Session)
	}
	if output.Query.LinesCaptured != 50 {
		t.Errorf("expected LinesCaptured=50, got %d", output.Query.LinesCaptured)
	}
	if output.Summary.TotalPanes != 2 {
		t.Errorf("expected TotalPanes=2, got %d", output.Summary.TotalPanes)
	}
}

// TestIsLiveBusyOverridesIdleVerdict_Codex pins the predicate that drives the
// #133 fix: when a Codex pane shows live "Working …" + "esc to interrupt"
// chrome, IsLiveBusy must return true so GetIsWorking forces IsWorking=true /
// IsIdle=false and re-derives the recommendation from the corrected state.
// Without this, the same scrollback that --robot-activity classifies as
// THINKING was being marked SAFE_TO_RESTART by --robot-is-working.
//
// The negative case pins that an idle codex prompt does not trip the override
// — otherwise every pane would be locked into the working bucket after any
// ambient match.
func TestIsLiveBusyOverridesIdleVerdict_Codex(t *testing.T) {
	scrollback := `> previous user prompt

• Working (4m 51s • esc to interrupt)
  Reading src/main.rs

`
	if !IsLiveBusy(scrollback, agent.AgentTypeCodex.String()) {
		t.Fatalf("IsLiveBusy(<codex working scrollback>, %q) = false, expected true; the live-window override would not fire and SAFE_TO_RESTART would leak through", agent.AgentTypeCodex.String())
	}

	idleScrollback := `> previous user prompt

  Done.

codex>
`
	if IsLiveBusy(idleScrollback, agent.AgentTypeCodex.String()) {
		t.Fatalf("IsLiveBusy(<idle codex prompt>, %q) = true, expected false; this would falsely keep idle panes out of the SAFE_TO_RESTART bucket", agent.AgentTypeCodex.String())
	}
}

// TestIsLiveBusy_Claude_DefersToOrderingAwareClassifier pins the Fix-6
// behavior: for Claude panes IsLiveBusy must defer to the ordering-aware
// agent.ClaudeActivelyWorking instead of a position-blind CategoryThinking
// match. A STALE spinner ("· Thundering… (4s)") can sit ABOVE a turn-ended
// completion line ("✻ Churned for 6s") in the live window; a bare
// CategoryThinking match would see the stale spinner and report busy,
// overriding the correct idle verdict so the dispatcher sees 0 idle agents
// after every burst and the swarm stalls with ready work waiting.
func TestIsLiveBusy_Claude_DefersToOrderingAwareClassifier(t *testing.T) {
	// Stale spinner ABOVE the most-recent completion line ⇒ turn ended ⇒ idle.
	staleSpinnerAboveCompletion := "· Thundering… (4s)\n" +
		"● final summary of the work\n" +
		"✻ Churned for 6s\n" +
		"────────────\n❯ \n────────────\n"
	if IsLiveBusy(staleSpinnerAboveCompletion, agent.AgentTypeClaudeCode.String()) {
		t.Fatalf("IsLiveBusy(<stale spinner above completion>, claude) = true, expected false; the position-blind CategoryThinking match would override the correct idle verdict and stall the swarm")
	}

	// A genuinely active spinner (most-recent dynamic marker) ⇒ working.
	activeSpinner := "✻ Cooked for 3m 1s\n" +
		"● starting next step\n" +
		"✻ Churning… (ctrl+c to interrupt · 4s)\n" +
		"────────────\n❯ \n"
	if !IsLiveBusy(activeSpinner, agent.AgentTypeClaudeCode.String()) {
		t.Fatalf("IsLiveBusy(<active claude spinner>, claude) = false, expected true; a mid-turn Claude pane must read busy")
	}

	// Alias "cc" must normalize to claude and take the same path.
	if IsLiveBusy(staleSpinnerAboveCompletion, "cc") {
		t.Fatalf("IsLiveBusy(<stale spinner above completion>, cc) = true, expected false; the cc alias must normalize to claude")
	}
}

func TestApplyLiveBusyOverrideRecommendationPrecedence(t *testing.T) {
	activeSpinner := "Error: prior command failed\n" +
		"✻ Germinating… (ctrl+c to interrupt · 5m 56s)\n" +
		"❯\n"
	tests := []struct {
		name  string
		state *agent.AgentState
		want  agent.Recommendation
	}{
		{
			name: "stale error does not override current work",
			state: &agent.AgentState{
				Type:      agent.AgentTypeClaudeCode,
				IsIdle:    true,
				IsInError: true,
			},
			want: agent.RecommendDoNotInterrupt,
		},
		{
			name: "rate limit retains precedence",
			state: &agent.AgentState{
				Type:          agent.AgentTypeClaudeCode,
				IsRateLimited: true,
				IsInError:     true,
			},
			want: agent.RecommendRateLimitedWait,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !applyLiveBusyOverride(activeSpinner, tt.state) {
				t.Fatal("expected live-busy override")
			}
			if !tt.state.IsWorking || tt.state.IsIdle || tt.state.IsInError {
				t.Fatalf("overridden state = %+v", tt.state)
			}
			if got := tt.state.GetRecommendation(); got != tt.want {
				t.Fatalf("recommendation = %q, want %q", got, tt.want)
			}
		})
	}

	currentError := &agent.AgentState{Type: agent.AgentTypeClaudeCode, IsInError: true}
	currentErrorOutput := "· Germinating… (5m 56s)\n" +
		"  ⎿ \u00a0Error: Exit code 1\n" +
		"     current command failed\n❯\n"
	if applyLiveBusyOverride(currentErrorOutput, currentError) {
		t.Fatal("current error after an older spinner must not be overridden")
	}
	if got := currentError.GetRecommendation(); got != agent.RecommendErrorState {
		t.Fatalf("current-error recommendation = %q, want %q", got, agent.RecommendErrorState)
	}

	codexError := &agent.AgentState{Type: agent.AgentTypeCodex, IsInError: true, IsIdle: true}
	codexOutput := "Error: current command failed\n• Working (4s · esc to interrupt)\ncodex>\n"
	if applyLiveBusyOverride(codexOutput, codexError) {
		t.Fatal("position-blind Codex working text must not override a current error")
	}
	if got := codexError.GetRecommendation(); got != agent.RecommendErrorState {
		t.Fatalf("codex current-error recommendation = %q, want %q", got, agent.RecommendErrorState)
	}
}

// TestIsLiveBusy_WildcardPatternsDocumentTheUserPaneSkipReason locks in the
// reason GetIsWorking gates the live-window override on `state.Type` being a
// known AI agent: the pattern library carries agent-agnostic CategoryThinking
// patterns (notably the braille spinner, which is unanchored and matches
// anywhere) that will fire on incidental shell output. If the override fired
// on user/unknown panes, a `tar`-style spinner or a starship-flavored prompt
// would falsely flip the pane into the working bucket. The GetIsWorking call
// site's shared isAIAgentLiveBusy guard filters for AI agents specifically so this never reaches
// PaneWorkStatus, but the predicate itself remains permissive — keep this
// test as the load-bearing canary if the wildcard set is ever rewritten.
func TestIsLiveBusy_WildcardPatternsDocumentTheUserPaneSkipReason(t *testing.T) {
	// Braille spinner pattern is `[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]` with Agent: "*", and it is
	// unanchored (no $ at the end of the regex), so any line containing one
	// of those chars matches. With a user hint the predicate still says
	// "live-busy" — so the GetIsWorking site must skip the override for
	// AgentTypeUser to avoid a false flip.
	shellScrollback := `$ tar -xzf data.tar.gz
extracting archive ⠋
`
	if !IsLiveBusy(shellScrollback, agent.AgentTypeUser.String()) {
		t.Fatalf("expected wildcard CategoryThinking match (braille_spinner) on shell scrollback with user hint; if this assertion changes, the GetIsWorking user-pane skip may no longer be needed")
	}
	if isAIAgentLiveBusy(shellScrollback, agent.AgentTypeUser.String()) {
		t.Fatal("shared live-busy guard must reject user panes even when wildcard thinking patterns match")
	}
}

// =============================================================================
// Window-aware pane selection (#170)
// =============================================================================
//
// In a window-per-agent layout (N windows × 1 pane all at window-local index 0)
// the legacy selection (`skip the global minimum index`) excluded every pane,
// and the bare-index response-map key collapsed N panes onto one entry. These
// tests pin both the single-window (unchanged) and multi-window (fixed)
// behavior of the pure selection helpers.

// singleWindowSession models the classic layout: one window, a control pane and
// two agent panes (pane-base-index = 1, so the control pane is index 1).
func singleWindowSession() []tmux.Pane {
	return []tmux.Pane{
		{ID: "%0", WindowIndex: 0, Index: 1, Title: "ctrl"},
		{ID: "%1", WindowIndex: 0, Index: 2, Title: "sess__cc_1"},
		{ID: "%2", WindowIndex: 0, Index: 3, Title: "sess__cod_1"},
	}
}

// windowPerAgentSession models the #170 layout: 3 windows, each with one pane at
// index 0.
func windowPerAgentSession() []tmux.Pane {
	return []tmux.Pane{
		{ID: "%0", WindowIndex: 0, Index: 0, Title: "sess__cc_1"},
		{ID: "%1", WindowIndex: 1, Index: 0, Title: "sess__cod_1"},
		{ID: "%2", WindowIndex: 2, Index: 0, Title: "sess__gmi_1"},
	}
}

func selectedTargets(sel []selectedPane) []string {
	out := make([]string, 0, len(sel))
	for _, s := range sel {
		out = append(out, s.target)
	}
	sort.Strings(out)
	return out
}

func TestSessionSpansMultipleWindows(t *testing.T) {
	if sessionSpansMultipleWindows(singleWindowSession()) {
		t.Error("single-window session reported as multi-window")
	}
	if !sessionSpansMultipleWindows(windowPerAgentSession()) {
		t.Error("window-per-agent session not reported as multi-window")
	}
	if sessionSpansMultipleWindows(nil) {
		t.Error("empty session reported as multi-window")
	}
}

func TestSelectIsWorkingPanes_SingleWindowDefaultSkipsControlPane(t *testing.T) {
	// Default selection (no requested panes): skip the window's lowest index
	// (control pane = 1), keep agent panes 2 and 3. This is unchanged behavior.
	sel := selectIsWorkingPanes("sess", singleWindowSession(), nil)
	if len(sel) != 2 {
		t.Fatalf("expected 2 non-control panes, got %d (%+v)", len(sel), sel)
	}
	gotTargets := selectedTargets(sel)
	wantTargets := []string{"sess:0.2", "sess:0.3"}
	for i, w := range wantTargets {
		if gotTargets[i] != w {
			t.Fatalf("target[%d] = %q, want %q (all: %v)", i, gotTargets[i], w, gotTargets)
		}
	}
}

func TestSelectIsWorkingPanes_WindowPerAgentDoesNotCollapse(t *testing.T) {
	// The bug: every pane shares index 0, so the global-minimum heuristic
	// excluded all of them (total_panes:0). Window-aware selection must keep
	// every window's single pane and address each by window.pane.
	sel := selectIsWorkingPanes("sess", windowPerAgentSession(), nil)
	if len(sel) != 3 {
		t.Fatalf("expected 3 panes (one per window), got %d (%+v)", len(sel), sel)
	}
	gotTargets := selectedTargets(sel)
	wantTargets := []string{"sess:0.0", "sess:1.0", "sess:2.0"}
	for i, w := range wantTargets {
		if gotTargets[i] != w {
			t.Fatalf("target[%d] = %q, want %q (all: %v)", i, gotTargets[i], w, gotTargets)
		}
	}
}

func TestSelectIsWorkingPanes_RequestedBareIndexIsWindowAware(t *testing.T) {
	// Topology-aware bare-index resolution (#172): on a window-per-agent layout
	// a bare `--panes=N` request selects the agent in WINDOW N (consistent with
	// the send/interrupt/restart-pane surfaces), not every window's index-N
	// pane. This makes single-agent dispatch possible on multi-window sessions
	// where every pane shares window-local index 0.
	sess := windowPerAgentSession()

	sel := selectIsWorkingPanes("sess", sess, []int{0})
	if len(sel) != 1 {
		t.Fatalf("expected requested index 0 to match only window 0, got %d", len(sel))
	}
	if got := selectedTargets(sel); got[0] != "sess:0.0" {
		t.Fatalf("target = %q, want %q", got[0], "sess:0.0")
	}

	sel = selectIsWorkingPanes("sess", sess, []int{2})
	if len(sel) != 1 {
		t.Fatalf("expected requested index 2 to match only window 2, got %d", len(sel))
	}
	if got := selectedTargets(sel); got[0] != "sess:2.0" {
		t.Fatalf("target = %q, want %q", got[0], "sess:2.0")
	}
}

func TestSelectIsWorkingPanes_RequestedMissingIndexIsNotFound(t *testing.T) {
	sel := selectIsWorkingPanes("sess", singleWindowSession(), []int{9})
	if len(sel) != 1 {
		t.Fatalf("expected 1 placeholder, got %d", len(sel))
	}
	if sel[0].found {
		t.Fatal("expected missing pane to be marked not-found")
	}
	if sel[0].Index != 9 {
		t.Fatalf("expected placeholder Index 9, got %d", sel[0].Index)
	}
}

func TestIsWorkingPaneKey(t *testing.T) {
	p := selectedPane{WindowIndex: 2, Index: 0}
	if got := isWorkingPaneKey(p, false); got != "0" {
		t.Errorf("single-window key = %q, want bare index %q", got, "0")
	}
	if got := isWorkingPaneKey(p, true); got != "2.0" {
		t.Errorf("multi-window key = %q, want %q", got, "2.0")
	}
}

// Helper function for substring matching
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && hasSubstr(s, substr)
}

func hasSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
