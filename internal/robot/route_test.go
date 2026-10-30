package robot

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestParseExcludePanes(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []int
		wantErr bool
	}{
		{
			name:    "empty string",
			input:   "",
			want:    nil,
			wantErr: false,
		},
		{
			name:    "single pane",
			input:   "1",
			want:    []int{1},
			wantErr: false,
		},
		{
			name:    "multiple panes",
			input:   "1,2,3",
			want:    []int{1, 2, 3},
			wantErr: false,
		},
		{
			name:    "with spaces",
			input:   "1, 2, 3",
			want:    []int{1, 2, 3},
			wantErr: false,
		},
		{
			name:    "invalid pane",
			input:   "1,abc,3",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "empty parts",
			input:   "1,,3",
			want:    []int{1, 3},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExcludePanes(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseExcludePanes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("ParseExcludePanes() = %v, want %v", got, tt.want)
					return
				}
				for i, v := range got {
					if v != tt.want[i] {
						t.Errorf("ParseExcludePanes()[%d] = %d, want %d", i, v, tt.want[i])
					}
				}
			}
		})
	}
}

// The transport supplies two physical panes with the same window-local
// index. Context collection, pane parsing, classification and route selection
// all use their production implementations.
func installRouteIdentityTmux(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("tmux fixture requires /bin/sh")
	}
	hermeticGlobalConfig(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	const script = `#!/bin/sh
case "$1" in
  has-session)
    if [ "$3" != "=route-identity" ]; then
      printf "can't find session: %s\n" "$3" >&2
      exit 1
    fi
    ;;
  list-panes)
    printf '%s\n' '%41_NTM_SEP_1_NTM_SEP_route-identity__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_1041_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0'
    printf '%s\n' '%51_NTM_SEP_1_NTM_SEP_route-identity__cc_2_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_1051_NTM_SEP_1_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0'
    ;;
  capture-pane) printf 'claude >\n' ;;
  *) printf 'unexpected tmux command: %s\n' "$*" >&2; exit 97 ;;
esac
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", path)
	previous := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = previous })
}

func TestRouteKeepsContextAndExclusionsWithPhysicalPane(t *testing.T) {
	installRouteIdentityTmux(t)
	prevProcess, prevCwd, prevPath := transcriptUsageForProcess, transcriptUsageForCwd, paneCurrentPath
	t.Cleanup(func() {
		transcriptUsageForProcess, transcriptUsageForCwd, paneCurrentPath = prevProcess, prevCwd, prevPath
	})
	paneCurrentPath = func(string) (string, bool) { return "", false }
	transcriptUsageForCwd = func(string, string) (*ntmctx.TranscriptUsage, bool) { return nil, false }
	transcriptUsageForProcess = func(_ string, pid int) (*ntmctx.TranscriptUsage, bool) {
		tokens, ok := map[int]int{1041: 190000, 1051: 10000}[pid]
		if !ok {
			return nil, false
		}
		return &ntmctx.TranscriptUsage{Tokens: tokens, ContextWindow: 200000, UpdatedAt: time.Now()}, true
	}

	contextOutput, err := GetContext("route-identity", 1000)
	if err != nil || !contextOutput.Success || len(contextOutput.Agents) != 2 {
		t.Fatalf("context = %+v, %v; want two agents", contextOutput, err)
	}
	for _, agent := range contextOutput.Agents {
		wantPane, wantUsage := "0.1", 95.0
		if agent.PaneID == "%51" {
			wantPane, wantUsage = "1.1", 5
		} else if agent.PaneID != "%41" {
			t.Fatalf("context returned unexpected physical identity: %+v", agent)
		}
		if agent.Pane != wantPane || agent.PaneIdx != 1 || agent.UsagePercent != wantUsage || agent.Source != "transcript" {
			t.Errorf("context agent = %+v, want %s at %.0f%%", agent, wantPane, wantUsage)
		}
	}

	scored, err := NewAgentScorer(DefaultRoutingConfig()).ScoreAgents("route-identity", "")
	if err != nil || len(scored) != 2 {
		t.Fatalf("scored agents = %+v, %v; want both physical panes", scored, err)
	}
	for _, agent := range scored {
		wantUsage, wantExcluded := 95.0, true
		if agent.PaneID == "%51" {
			wantUsage, wantExcluded = 5, false
		}
		if agent.ContextUsage != wantUsage || agent.Excluded != wantExcluded {
			t.Errorf("scored agent = %+v, want context %.0f%% excluded=%t", agent, wantUsage, wantExcluded)
		}
	}

	tests := []struct {
		name       string
		threshold  float64
		excludeIDs []string
		excludeIdx []int
		want       string
		candidates int
	}{
		{name: "full pane is excluded", threshold: 85, want: "%51", candidates: 1},
		{name: "threshold boundary is eligible", threshold: 95, want: "%41", candidates: 2},
		{name: "exclude first window only", threshold: 100, excludeIDs: []string{"%41"}, want: "%51", candidates: 1},
		{name: "exclude second window only", threshold: 100, excludeIDs: []string{"%51"}, want: "%41", candidates: 1},
		{name: "numeric exclusion still matches local index", threshold: 100, excludeIdx: []int{1}, candidates: 0},
		{name: "unknown identity excludes nothing", threshold: 100, excludeIDs: []string{"%99"}, want: "%41", candidates: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Routing.ExcludeContextAbove = tt.threshold
			opts := RouteOptions{
				Session: "route-identity", Strategy: StrategySticky, LastAgent: "%41",
				ExcludePanes: tt.excludeIdx, ExcludePaneIDs: tt.excludeIDs,
				Config: cfg, NoPersist: true,
			}
			output, code := GetRoute(opts)
			if code != 0 || !output.Success || len(output.Candidates) != tt.candidates {
				t.Fatalf("route = %+v, code %d; want %d candidates", output, code, tt.candidates)
			}
			recommendation, err := GetRouteRecommendation(opts)
			if err != nil {
				t.Fatal(err)
			}
			for surface, rec := range map[string]*RouteRecommendation{"robot": output.Recommendation, "send": recommendation} {
				if tt.want == "" {
					if rec != nil {
						t.Errorf("%s recommended excluded pane: %+v", surface, rec)
					}
					continue
				}
				if rec == nil || rec.PaneID != tt.want {
					t.Errorf("%s recommendation = %+v, want %s", surface, rec, tt.want)
				}
			}
			if tt.threshold == 85 && (len(output.Excluded) != 1 || output.Excluded[0].PaneID != "%41" || !strings.Contains(output.Excluded[0].Reason, "context")) {
				t.Errorf("full pane missing from context exclusions: %+v", output.Excluded)
			}
			if tt.want != "" {
				wantCommand := fmt.Sprintf("ntm --robot-send=route-identity --panes=%s --msg='YOUR_MESSAGE'", tt.want)
				if output.AgentHints == nil || output.AgentHints.SendCommand != wantCommand {
					t.Errorf("route hint lost physical target: %+v, want %q", output.AgentHints, wantCommand)
				}
			}
		})
	}
	if usage := getContextUsageByPane("missing-session"); usage != nil {
		t.Errorf("unavailable context = %v, want nil", usage)
	}
}

func TestRouteOptions(t *testing.T) {
	opts := RouteOptions{
		Session:      "test",
		Strategy:     StrategyLeastLoaded,
		AgentType:    "claude",
		ExcludePanes: []int{1, 2},
	}

	if opts.Session != "test" {
		t.Errorf("Session = %s, want 'test'", opts.Session)
	}
	if opts.Strategy != StrategyLeastLoaded {
		t.Errorf("Strategy = %s, want %s", opts.Strategy, StrategyLeastLoaded)
	}
	if opts.AgentType != "claude" {
		t.Errorf("AgentType = %s, want 'claude'", opts.AgentType)
	}
	if len(opts.ExcludePanes) != 2 {
		t.Errorf("ExcludePanes len = %d, want 2", len(opts.ExcludePanes))
	}
}

func TestRoutePaneAgentTypePrefersParsedPaneType(t *testing.T) {

	pane := tmux.Pane{
		Title:   "custom title",
		Type:    tmux.AgentClaude,
		Command: "claude --print",
	}

	if got := routePaneAgentType(pane); got != "claude" {
		t.Fatalf("routePaneAgentType() = %q, want %q", got, "claude")
	}
}

func TestRoutePaneAgentTypeFallsBackToTitle(t *testing.T) {

	pane := tmux.Pane{
		Title:   "myproj__cod_2",
		Type:    tmux.AgentUnknown,
		Command: "zsh",
	}

	if got := routePaneAgentType(pane); got != "codex" {
		t.Fatalf("routePaneAgentType() = %q, want %q", got, "codex")
	}
}

func TestRouteOutput(t *testing.T) {
	output := RouteOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "myproject",
		Strategy:      StrategyLeastLoaded,
		Candidates:    []RouteCandidate{},
		Excluded:      []RouteExcluded{},
	}

	if !output.Success {
		t.Error("Success should be true")
	}
	if output.Session != "myproject" {
		t.Errorf("Session = %s, want 'myproject'", output.Session)
	}
	if output.Strategy != StrategyLeastLoaded {
		t.Errorf("Strategy = %s, want %s", output.Strategy, StrategyLeastLoaded)
	}
}

func TestRouteRecommendation(t *testing.T) {
	rec := RouteRecommendation{
		PaneID:       "cc_1",
		PaneIndex:    1,
		AgentType:    "cc",
		Score:        85.5,
		Reason:       "highest score",
		ContextUsage: 30.0,
		State:        "WAITING",
	}

	if rec.PaneID != "cc_1" {
		t.Errorf("PaneID = %s, want 'cc_1'", rec.PaneID)
	}
	if rec.PaneIndex != 1 {
		t.Errorf("PaneIndex = %d, want 1", rec.PaneIndex)
	}
	if rec.Score != 85.5 {
		t.Errorf("Score = %f, want 85.5", rec.Score)
	}
}

func TestRouteCandidate(t *testing.T) {
	candidate := RouteCandidate{
		PaneID:       "cc_2",
		PaneIndex:    2,
		AgentType:    "cc",
		Score:        70.0,
		ContextUsage: 50.0,
		State:        "WAITING",
		StateScore:   100.0,
		RecencyScore: 50.0,
	}

	if candidate.StateScore != 100.0 {
		t.Errorf("StateScore = %f, want 100.0", candidate.StateScore)
	}
	if candidate.RecencyScore != 50.0 {
		t.Errorf("RecencyScore = %f, want 50.0", candidate.RecencyScore)
	}
}

func TestRouteExcluded(t *testing.T) {
	excluded := RouteExcluded{
		PaneID:    "cc_3",
		PaneIndex: 3,
		AgentType: "cc",
		Reason:    "agent in ERROR state",
		State:     "ERROR",
	}

	if excluded.Reason != "agent in ERROR state" {
		t.Errorf("Reason = %s, want 'agent in ERROR state'", excluded.Reason)
	}
	if excluded.State != "ERROR" {
		t.Errorf("State = %s, want 'ERROR'", excluded.State)
	}
}

func TestRouteAgentHints(t *testing.T) {
	hints := RouteAgentHints{
		Summary:     "Route to cc (pane %41) with score 85.5 - WAITING",
		SendCommand: "ntm --robot-send=test --panes=%41 --msg='YOUR_MESSAGE'",
		Suggestions: []string{"Primary strategy succeeded"},
	}

	if hints.Summary == "" {
		t.Error("Summary should not be empty")
	}
	if hints.SendCommand == "" {
		t.Error("SendCommand should not be empty")
	}
	if len(hints.Suggestions) != 1 {
		t.Errorf("Suggestions len = %d, want 1", len(hints.Suggestions))
	}
}

func TestGenerateRouteHints(t *testing.T) {
	t.Run("with recommendation", func(t *testing.T) {
		opts := RouteOptions{Session: "test"}
		output := RouteOutput{
			Recommendation: &RouteRecommendation{
				PaneID:    "%41",
				PaneIndex: 1,
				AgentType: "cc",
				Score:     85.5,
				State:     "WAITING",
			},
		}

		hints := generateRouteHints(opts, output)
		if hints.Summary == "" {
			t.Error("Summary should not be empty")
		}
		if hints.SendCommand != "ntm --robot-send=test --panes=%41 --msg='YOUR_MESSAGE'" {
			t.Errorf("SendCommand = %q, want the exact physical pane ID", hints.SendCommand)
		}
	})

	t.Run("no candidates", func(t *testing.T) {
		opts := RouteOptions{Session: "test"}
		output := RouteOutput{
			Candidates: []RouteCandidate{},
			Excluded: []RouteExcluded{
				{PaneID: "cc_1", Reason: "excluded"},
			},
		}

		hints := generateRouteHints(opts, output)
		if hints.Summary == "" {
			t.Error("Summary should not be empty")
		}
		if len(hints.Suggestions) == 0 {
			t.Error("Suggestions should not be empty for no agents")
		}
	})

	t.Run("fallback used", func(t *testing.T) {
		opts := RouteOptions{Session: "test"}
		output := RouteOutput{
			FallbackUsed: true,
			Candidates:   []RouteCandidate{{PaneID: "cc_1"}},
		}

		hints := generateRouteHints(opts, output)
		found := false
		for _, s := range hints.Suggestions {
			if s == "Primary strategy failed - fallback was used" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Should mention fallback was used")
		}
	})

	t.Run("affinity fallback explains the missing holder", func(t *testing.T) {
		opts := RouteOptions{Session: "test", Strategy: StrategyAffinity}
		output := RouteOutput{
			FallbackUsed: true,
			Candidates:   []RouteCandidate{{PaneID: "cc_1"}},
		}

		hints := generateRouteHints(opts, output)
		joined := strings.Join(hints.Suggestions, "\n")
		if !strings.Contains(joined, "No available agent holds Agent Mail reservations") || !strings.Contains(joined, "least-loaded fallback") {
			t.Errorf("affinity fallback suggestions = %q, want the no-holder explanation", hints.Suggestions)
		}
	})
}

func TestRouteStrategyNames(t *testing.T) {
	names := strategyNames()
	if len(names) != 8 {
		t.Errorf("strategyNames() returned %d names, want 8", len(names))
	}

	expected := map[string]bool{
		"least-loaded":          true,
		"first-available":       true,
		"round-robin":           true,
		"round-robin-available": true,
		"random":                true,
		"sticky":                true,
		"explicit":              true,
		"affinity":              true,
	}

	for _, name := range names {
		if !expected[name] {
			t.Errorf("Unexpected strategy name: %s", name)
		}
	}
}

func TestGetRoute_MissingSession(t *testing.T) {
	out, code := GetRoute(RouteOptions{Strategy: StrategyLeastLoaded})
	if code == 0 {
		t.Fatal("expected non-zero exit code for missing session")
	}
	if out.Success {
		t.Error("expected Success=false for missing session")
	}
	if out.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("ErrorCode = %q, want %q", out.ErrorCode, ErrCodeInvalidFlag)
	}
	if !strings.Contains(out.Error, "session name") {
		t.Errorf("Error = %q", out.Error)
	}
}

func TestGetRoute_InvalidStrategy(t *testing.T) {
	out, code := GetRoute(RouteOptions{
		Session:  "fake-session",
		Strategy: StrategyName("bogus"),
	})
	if code == 0 {
		t.Fatal("expected non-zero exit code for invalid strategy")
	}
	if out.Success {
		t.Error("expected Success=false for invalid strategy")
	}
	if out.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("ErrorCode = %q, want %q", out.ErrorCode, ErrCodeInvalidFlag)
	}
	if !strings.Contains(out.Error, "invalid strategy") {
		t.Errorf("Error = %q", out.Error)
	}
}

// TestGetRoute_AffinityRequiresMessage: affinity ranks agents by the files
// the routed message names, so an affinity route with no message could only
// ever fall back to least-loaded. It must fail as INVALID_ARGS (before any
// tmux lookup) rather than quietly answer a different question.
func TestGetRoute_AffinityRequiresMessage(t *testing.T) {
	for _, prompt := range []string{"", "   \n\t"} {
		out, code := GetRoute(RouteOptions{
			Session:  "fake-session",
			Strategy: StrategyAffinity,
			Prompt:   prompt,
		})
		if code == 0 || out.Success {
			t.Fatalf("prompt %q: affinity route without a message succeeded (code %d)", prompt, code)
		}
		if out.ErrorCode != ErrCodeInvalidArgs {
			t.Errorf("prompt %q: ErrorCode = %q, want %q", prompt, out.ErrorCode, ErrCodeInvalidArgs)
		}
		if !strings.Contains(out.Error, "needs the message being routed") || !strings.Contains(out.Hint, "--msg") {
			t.Errorf("prompt %q: error/hint do not explain the missing message: %q / %q", prompt, out.Error, out.Hint)
		}
	}

	// With a message the request is valid and proceeds to the session check.
	session := fmt.Sprintf("ntm-missing-%d", time.Now().UnixNano())
	out, _ := GetRoute(RouteOptions{Session: session, Strategy: StrategyAffinity, Prompt: "Fix internal/robot/routing.go"})
	if out.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("affinity route with a message: ErrorCode = %q, want %q (validation must pass)", out.ErrorCode, ErrCodeSessionNotFound)
	}
}

func TestGetRoute_SessionNotFound(t *testing.T) {
	session := fmt.Sprintf("ntm-missing-%d", time.Now().UnixNano())
	out, code := GetRoute(RouteOptions{
		Session:  session,
		Strategy: StrategyLeastLoaded,
	})
	if code == 0 {
		t.Fatal("expected non-zero exit code for missing session")
	}
	if out.Success {
		t.Error("expected Success=false for missing session")
	}
	if out.ErrorCode != ErrCodeSessionNotFound {
		t.Errorf("ErrorCode = %q, want %q", out.ErrorCode, ErrCodeSessionNotFound)
	}
	if !strings.Contains(out.Error, session) {
		t.Errorf("Error should mention session name, got %q", out.Error)
	}
}

func TestGetRouteRecommendation_Errors(t *testing.T) {
	if _, err := GetRouteRecommendation(RouteOptions{}); err == nil {
		t.Fatal("expected error for missing session")
	}

	if _, err := GetRouteRecommendation(RouteOptions{
		Session:  "fake-session",
		Strategy: StrategyName("bogus"),
	}); err == nil {
		t.Fatal("expected error for invalid strategy")
	}

	if _, err := GetRouteRecommendation(RouteOptions{
		Session:  "fake-session",
		Strategy: StrategyAffinity,
	}); err == nil || !strings.Contains(err.Error(), "needs the message being routed") {
		t.Fatalf("affinity recommendation without a prompt: err = %v, want the missing-message error", err)
	}

	session := fmt.Sprintf("ntm-missing-%d", time.Now().UnixNano())
	if _, err := GetRouteRecommendation(RouteOptions{
		Session:  session,
		Strategy: StrategyLeastLoaded,
	}); err == nil {
		t.Fatal("expected error for missing session")
	}
}

// TestGetRoute_AffinityRoutesToReservationHolder is the end-to-end proof for
// the affinity strategy on the --robot-route surface: a real tmux session
// with two claude panes, a stub Agent Mail server holding a live reservation
// on internal/robot/*.go for GreenCastle, and the persisted session agent
// registry mapping GreenCastle to the pane least-loaded passes over. With
// [routing] affinity_enabled left off, affinity must still route a prompt
// naming a reserved file to the holder as its primary selection, and a
// prompt naming no reserved file must fall back to least-loaded, saying so.
func TestGetRoute_AffinityRoutesToReservationHolder(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	hermeticGlobalConfig(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	resetSharedReservationCaches(t)

	session := fmt.Sprintf("ntm-affinity-route-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, ""); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })
	if _, err := tmux.DefaultClient.Run("split-window", "-d", "-t", session); err != nil {
		t.Fatalf("split window: %v", err)
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("get panes: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("setup produced %d panes, want 2: %+v", len(panes), panes)
	}
	for _, pane := range panes {
		if _, err := tmux.DefaultClient.Run("respawn-pane", "-k", "-t", pane.ID, "cat"); err != nil {
			t.Fatalf("respawn %s: %v", pane.ID, err)
		}
		if err := tmux.DefaultClient.SetPaneAgentType(pane.ID, tmux.AgentClaude); err != nil {
			t.Fatalf("record agent type on %s: %v", pane.ID, err)
		}
	}

	expires := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	created := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	server := newStubAgentMailReservationServer(t, fmt.Sprintf(
		`[{"id":1,"agent":"GreenCastle","path_pattern":"internal/robot/*.go","exclusive":true,"reason":"affinity","created_ts":%q,"expires_ts":%q}]`,
		created, expires))
	cfg := affinityWireConfig(server.URL)
	cfg.Routing.AffinityEnabled = false // the affinity strategy is its own opt-in

	baseline, code := GetRoute(RouteOptions{Session: session, Strategy: StrategyLeastLoaded, Config: cfg})
	if code != 0 || baseline.Recommendation == nil || len(baseline.Candidates) != 2 {
		t.Fatalf("least-loaded baseline = code %d recommendation %+v candidates %+v excluded %+v, want a pick over 2 candidates",
			code, baseline.Recommendation, baseline.Candidates, baseline.Excluded)
	}
	holderID := ""
	for _, candidate := range baseline.Candidates {
		if candidate.PaneID != baseline.Recommendation.PaneID {
			holderID = candidate.PaneID
		}
	}
	if err := agentmail.SaveSessionAgentRegistry(&agentmail.SessionAgentRegistry{
		SessionName: session,
		ProjectKey:  t.TempDir(),
		PaneIDMap:   map[string]string{holderID: "GreenCastle"},
	}); err != nil {
		t.Fatalf("save session agent registry: %v", err)
	}

	routed, code := GetRoute(RouteOptions{
		Session:  session,
		Strategy: StrategyAffinity,
		Prompt:   "Fix the scoring bug in internal/robot/routing.go",
		Config:   cfg,
	})
	if code != 0 || routed.Recommendation == nil {
		t.Fatalf("affinity route = code %d %+v, want a recommendation", code, routed)
	}
	if routed.Recommendation.PaneID != holderID || routed.FallbackUsed {
		t.Fatalf("affinity recommendation = %s (fallback_used %v), want reservation holder %s as the primary selection (least-loaded picked %s)",
			routed.Recommendation.PaneID, routed.FallbackUsed, holderID, baseline.Recommendation.PaneID)
	}
	for _, candidate := range routed.Candidates {
		want := 0.0
		if candidate.PaneID == holderID {
			want = 1
		}
		if candidate.AffinityMatch != want {
			t.Errorf("candidate %s affinity_match = %v, want %v", candidate.PaneID, candidate.AffinityMatch, want)
		}
	}
	if w := routed.Recommendation.ReservationWarning; w != nil {
		t.Errorf("routing to the only holder warned %+v, want no reservation_warning", w)
	}

	// With the holder excluded the pick edits GreenCastle's reserved file.
	other, code := GetRoute(RouteOptions{
		Session:        session,
		Strategy:       StrategyAffinity,
		Prompt:         "Fix the scoring bug in internal/robot/routing.go",
		Config:         cfg,
		ExcludePaneIDs: []string{holderID},
	})
	if code != 0 || other.Recommendation == nil || other.Recommendation.PaneID == holderID {
		t.Fatalf("route excluding the holder = code %d %+v, want a non-holder recommendation", code, other.Recommendation)
	}
	w := other.Recommendation.ReservationWarning
	if w == nil || w.SelectedHas || !reflect.DeepEqual(w.Holders, []string{"GreenCastle"}) ||
		!reflect.DeepEqual(w.Paths, []string{"internal/robot/routing.go"}) {
		t.Fatalf("reservation_warning = %+v, want GreenCastle holding internal/robot/routing.go", w)
	}
	if other.AgentHints == nil || !strings.Contains(strings.Join(other.AgentHints.Suggestions, "\n"), "reserved by GreenCastle") {
		t.Errorf("route hints %+v do not mention the reservation holder", other.AgentHints)
	}

	unreserved, code := GetRoute(RouteOptions{
		Session:  session,
		Strategy: StrategyAffinity,
		Prompt:   "Update docs/README.md",
		Config:   cfg,
	})
	if code != 0 || unreserved.Recommendation == nil {
		t.Fatalf("unreserved affinity route = code %d %+v, want a recommendation", code, unreserved)
	}
	if !unreserved.FallbackUsed || unreserved.Recommendation.PaneID != baseline.Recommendation.PaneID {
		t.Fatalf("unreserved affinity route = %s (fallback_used %v), want the least-loaded fallback %s",
			unreserved.Recommendation.PaneID, unreserved.FallbackUsed, baseline.Recommendation.PaneID)
	}
}

// TestRouteReservationWarning pins when a routing pick reports reservations:
// only when another agent holds a file the message names, never for the sole
// holder, and never without a wired reservation cache.
func TestRouteReservationWarning(t *testing.T) {
	cfg := DefaultRoutingConfig()
	cfg.AgentMail.Enabled = true
	scorer := NewAgentScorer(cfg)
	cache := NewReservationCache(nil, "/test", 30*time.Second)
	cache.pathToAgents = map[string][]string{
		"internal/robot/*.go": {"GreenCastle"},
		"docs/*.md":           {"GreenCastle", "BlueLake"},
	}
	scorer.SetReservationCache(cache)
	scorer.MapPaneToAgent("%1", "GreenCastle")
	scorer.MapPaneToAgent("%2", "RedFox")

	if w := routeReservationWarning(scorer, "Fix internal/robot/routing.go", "%1"); w != nil {
		t.Errorf("sole holder selected: warning %+v, want nil", w)
	}
	w := routeReservationWarning(scorer, "Fix internal/robot/routing.go", "%2")
	if w == nil || w.SelectedHas || !reflect.DeepEqual(w.Holders, []string{"GreenCastle"}) {
		t.Errorf("other agent selected: warning %+v, want GreenCastle as the holder", w)
	}
	w = routeReservationWarning(scorer, "Update docs/README.md", "%1")
	if w == nil || !w.SelectedHas || !reflect.DeepEqual(w.Holders, []string{"BlueLake", "GreenCastle"}) {
		t.Errorf("shared reservation: warning %+v, want sorted holders [BlueLake GreenCastle] incl. the selected agent", w)
	}
	if w := routeReservationWarning(scorer, "Explain the router", "%2"); w != nil {
		t.Errorf("no files named: warning %+v, want nil", w)
	}
	if w := routeReservationWarning(NewAgentScorer(cfg), "Fix internal/robot/routing.go", "%2"); w != nil {
		t.Errorf("no reservation cache: warning %+v, want nil", w)
	}
}

func TestRoutingPreservesUnsubmittedAndQueuedInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("routing fixture requires a POSIX shell")
	}
	previousProcess, previousCwd, previousPath := transcriptUsageForProcess, transcriptUsageForCwd, paneCurrentPath
	t.Cleanup(func() {
		transcriptUsageForProcess, transcriptUsageForCwd, paneCurrentPath = previousProcess, previousCwd, previousPath
	})
	transcriptUsageForProcess = func(string, int) (*ntmctx.TranscriptUsage, bool) { return nil, false }
	transcriptUsageForCwd = func(string, string) (*ntmctx.TranscriptUsage, bool) { return nil, false }
	paneCurrentPath = func(string) (string, bool) { return "", false }

	box := func(rows ...string) string {
		capture := "╭──────────────────────────────────────╮\n"
		for _, row := range rows {
			capture += "│ " + row + " │\n"
		}
		return capture + "╰──────────────────────────────────────╯\n"
	}
	fixture := func(name string) string {
		data, err := os.ReadFile(filepath.Join("..", "agent", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	empty := map[string]string{
		"claude": "❯ \n",
		"codex":  "› \n",
		"grok":   box("❯ "),
		"omp":    fixture("omp_nerd_idle_done.txt"),
	}
	const queued = "❯ \nPress up to edit queued messages\n"
	cases := []struct {
		name, agent, capture, other     string
		firstReason, secondReason, want string
		height                          int
	}{
		{name: "Claude draft", agent: "claude", capture: "❯ Keep this draft\n", firstReason: "unsubmitted input", want: "%42"},
		{name: "wrapped Claude draft", agent: "claude", capture: box("❯ ", "  the draft continues below the marker"), firstReason: "unsubmitted input", want: "%42"},
		{name: "Codex draft", agent: "codex", capture: "› Keep this draft\n", firstReason: "unsubmitted input", want: "%42"},
		{name: "Grok draft", agent: "grok", capture: box("❯ Keep this draft"), firstReason: "unsubmitted input", want: "%42"},
		{name: "Omp draft", agent: "omp", capture: fixture("omp_nerd_draft.txt"), firstReason: "unsubmitted input", want: "%42"},
		{name: "queued follow-up", agent: "claude", capture: queued, firstReason: "queued messages", want: "%42"},
		{name: "all occupied", agent: "claude", capture: "❯ Keep this draft\n", other: queued, firstReason: "unsubmitted input", secondReason: "queued messages"},
		{name: "history above empty composer", agent: "claude", capture: "❯ Previous submitted prompt\nCompleted response\n❯ \n", want: "%41"},
		{name: "Claude placeholder", agent: "claude", capture: "❯ Try \"fix the auth bug\"\n", want: "%41"},
		{name: "Codex placeholder", agent: "codex", capture: "› Ask Codex to do anything\n", want: "%41"},
		{name: "queued footer outside visible pane", agent: "claude", capture: "Press up to edit queued messages\nold output\ncurrent output\n❯ \n", height: 3, want: "%41"},
		{name: "queued footer outside live tail", agent: "claude", capture: "Press up to edit queued messages\n" + strings.Repeat("old output\n", 12) + "❯ \n", want: "%41"},
	}
	const script = `#!/bin/sh
set -eu
case "$1" in
  has-session) [ "$3" = '=route-composer' ] ;;
  list-panes)
    printf '%s\n' "%41_NTM_SEP_1_NTM_SEP_route-composer__${NTM_TEST_ROUTE_AGENT}_1_NTM_SEP_${NTM_TEST_ROUTE_AGENT}_NTM_SEP_240_NTM_SEP_${NTM_TEST_ROUTE_HEIGHT}_NTM_SEP_1_NTM_SEP_1041_NTM_SEP_0_NTM_SEP_${NTM_TEST_ROUTE_AGENT}_NTM_SEP__NTM_SEP__NTM_SEP_0"
    printf '%s\n' "%42_NTM_SEP_2_NTM_SEP_route-composer__${NTM_TEST_ROUTE_AGENT}_2_NTM_SEP_${NTM_TEST_ROUTE_AGENT}_NTM_SEP_240_NTM_SEP_${NTM_TEST_ROUTE_HEIGHT}_NTM_SEP_0_NTM_SEP_1042_NTM_SEP_0_NTM_SEP_${NTM_TEST_ROUTE_AGENT}_NTM_SEP__NTM_SEP__NTM_SEP_0"
    ;;
  capture-pane)
    case "$3" in
      %41|%42) cat "$NTM_TEST_ROUTE_COMPOSER_ROOT/$3" ;;
      *) exit 98 ;;
    esac
    ;;
  *) printf 'unexpected tmux command: %s\n' "$*" >&2; exit 97 ;;
esac
`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hermeticGlobalConfig(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			root := t.TempDir()
			other := tc.other
			if other == "" {
				other = empty[tc.agent]
			}
			for name, capture := range map[string]string{"%41": tc.capture, "%42": other} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(capture), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			binary := filepath.Join(root, "tmux")
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			height := tc.height
			if height == 0 {
				height = 60
			}
			t.Setenv("NTM_TMUX_BINARY", binary)
			t.Setenv("NTM_TEST_ROUTE_COMPOSER_ROOT", root)
			t.Setenv("NTM_TEST_ROUTE_AGENT", tc.agent)
			t.Setenv("NTM_TEST_ROUTE_HEIGHT", fmt.Sprint(height))
			previousClient := tmux.DefaultClient
			tmux.DefaultClient = tmux.NewClient("")
			t.Cleanup(func() { tmux.DefaultClient = previousClient })

			// Composer occupancy is a routing constraint even when the caller
			// deliberately permits busy, rate-limited, and error-state agents.
			cfg := config.Default()
			cfg.Routing.ExcludeIfGenerating = false
			cfg.Routing.ExcludeIfRateLimited = false
			cfg.Routing.ExcludeIfErrorState = false
			cfg.Routing.ExcludeContextAbove = 100
			scorer := NewAgentScorerFromConfig(cfg)
			scored, err := scorer.ScoreAgents("route-composer", "")
			if err != nil || len(scored) != 2 {
				t.Fatalf("scored agents = %+v, %v; want both physical panes", scored, err)
			}
			reasons := map[string]string{"%41": tc.firstReason, "%42": tc.secondReason}
			wantCandidates := 0
			for _, agent := range scored {
				reason := reasons[agent.PaneID]
				if reason == "" {
					wantCandidates++
				}
				if agent.Excluded != (reason != "") || (reason != "" && !strings.Contains(agent.ExcludeReason, reason)) {
					t.Fatalf("scorer returned %+v, want exclusion reason %q", agent, reason)
				}
			}
			opts := RouteOptions{
				Session: "route-composer", Strategy: StrategySticky, LastAgent: "%41",
				Config: cfg, NoPersist: true,
			}
			output, code := GetRoute(opts)
			if code != 0 || !output.Success || len(output.Candidates) != wantCandidates || len(output.Excluded) != 2-wantCandidates {
				t.Fatalf("robot route = %+v, code %d; want %d candidates", output, code, wantCandidates)
			}
			for _, excluded := range output.Excluded {
				if reason := reasons[excluded.PaneID]; reason == "" || !strings.Contains(excluded.Reason, reason) {
					t.Errorf("robot exclusion = %+v, want reason %q", excluded, reason)
				}
			}
			recommendation, err := GetRouteRecommendation(opts)
			if err != nil {
				t.Fatal(err)
			}
			for surface, rec := range map[string]*RouteRecommendation{"robot": output.Recommendation, "send": recommendation} {
				if tc.want == "" {
					if rec != nil {
						t.Errorf("%s selected an occupied pane: %+v", surface, rec)
					}
				} else if rec == nil || rec.PaneID != tc.want {
					t.Errorf("%s recommendation = %+v, want %s", surface, rec, tc.want)
				}
			}
			if tc.firstReason != "" {
				if err := os.WriteFile(filepath.Join(root, "%41"), []byte(empty[tc.agent]), 0o600); err != nil {
					t.Fatal(err)
				}
				// Reuse the classifier to prove occupancy is refreshed rather
				// than remaining latched after the user clears the composer.
				refreshed, err := scorer.ScoreAgents("route-composer", "")
				if err != nil || len(refreshed) != 2 {
					t.Fatalf("refreshed scores = %+v, %v", refreshed, err)
				}
				for _, agent := range refreshed {
					if agent.PaneID == "%41" && (agent.Excluded || agent.UnsubmittedInput || agent.QueuedMessages) {
						t.Fatalf("cleared composer stayed occupied: %+v", agent)
					}
				}
				recovered, err := GetRouteRecommendation(opts)
				if err != nil || recovered == nil || recovered.PaneID != "%41" {
					t.Fatalf("cleared pane was not selectable: %+v, %v", recovered, err)
				}
			}
		})
	}
}
