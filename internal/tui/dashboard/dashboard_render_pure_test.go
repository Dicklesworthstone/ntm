package dashboard

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ---------------------------------------------------------------------------
// rchStatusActive — pure function
// ---------------------------------------------------------------------------

func TestRchStatusActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status *tools.RCHStatus
		want   bool
	}{
		{"nil status", nil, false},
		{"empty workers", &tools.RCHStatus{}, false},
		{"worker no build no queue", &tools.RCHStatus{
			Workers: []tools.RCHWorker{{Name: "w1"}},
		}, false},
		{"worker with current build", &tools.RCHStatus{
			Workers: []tools.RCHWorker{{Name: "w1", CurrentBuild: "cargo build"}},
		}, true},
		{"worker with queue", &tools.RCHStatus{
			Workers: []tools.RCHWorker{{Name: "w1", Queue: 3}},
		}, true},
		{"whitespace only build", &tools.RCHStatus{
			Workers: []tools.RCHWorker{{Name: "w1", CurrentBuild: "   \t  "}},
		}, false},
		{"multiple workers first active", &tools.RCHStatus{
			Workers: []tools.RCHWorker{
				{Name: "w1", CurrentBuild: "make"},
				{Name: "w2"},
			},
		}, true},
		{"multiple workers second active", &tools.RCHStatus{
			Workers: []tools.RCHWorker{
				{Name: "w1"},
				{Name: "w2", Queue: 1},
			},
		}, true},
		{"multiple workers none active", &tools.RCHStatus{
			Workers: []tools.RCHWorker{
				{Name: "w1", CurrentBuild: ""},
				{Name: "w2", Queue: 0},
			},
		}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := rchStatusActive(tc.status)
			if got != tc.want {
				t.Errorf("rchStatusActive() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// logPanelSize — pure function with interface
// ---------------------------------------------------------------------------

type mockSizedPanel struct {
	w, h int
}

func (m mockSizedPanel) Width() int  { return m.w }
func (m mockSizedPanel) Height() int { return m.h }

func TestLogPanelSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		pname string
		panel sizedPanel
		want  string
	}{
		{"nil panel", "main", nil, "main=0x0"},
		{"zero size", "detail", mockSizedPanel{0, 0}, "detail=0x0"},
		{"normal size", "grid", mockSizedPanel{80, 24}, "grid=80x24"},
		{"large size", "output", mockSizedPanel{200, 60}, "output=200x60"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := logPanelSize(tc.pname, tc.panel)
			if got != tc.want {
				t.Errorf("logPanelSize(%q, ...) = %q, want %q", tc.pname, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// hasMetricsData — pure function
// ---------------------------------------------------------------------------

func TestHasMetricsData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data panels.MetricsData
		want bool
	}{
		{"all nil", panels.MetricsData{}, false},
		{"coverage only", panels.MetricsData{Coverage: &ensemble.CoverageReport{}}, true},
		{"redundancy only", panels.MetricsData{Redundancy: &ensemble.RedundancyAnalysis{}}, true},
		{"velocity only", panels.MetricsData{Velocity: &ensemble.VelocityReport{}}, true},
		{"conflicts only", panels.MetricsData{Conflicts: &ensemble.ConflictDensity{}}, true},
		{"all set", panels.MetricsData{
			Coverage:   &ensemble.CoverageReport{},
			Redundancy: &ensemble.RedundancyAnalysis{},
			Velocity:   &ensemble.VelocityReport{},
			Conflicts:  &ensemble.ConflictDensity{},
		}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := hasMetricsData(tc.data)
			if got != tc.want {
				t.Errorf("hasMetricsData() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// slicesEqual — pure function
// ---------------------------------------------------------------------------

func TestSlicesEqual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []string{}, []string{}, true},
		{"nil vs empty", nil, []string{}, true},
		{"equal single", []string{"a"}, []string{"a"}, true},
		{"equal multi", []string{"a", "b", "c"}, []string{"a", "b", "c"}, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"same length different content", []string{"a", "b"}, []string{"a", "c"}, false},
		{"order matters", []string{"a", "b"}, []string{"b", "a"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := slicesEqual(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("slicesEqual(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// recordCostOutputDelta — method with pure dependencies
// ---------------------------------------------------------------------------

func TestRecordCostOutputDelta(t *testing.T) {
	t.Parallel()

	t.Run("empty pane ID returns early", func(t *testing.T) {
		t.Parallel()
		m := &Model{}
		m.recordCostOutputDelta("", "gpt-4", "old", "new")
		if m.costOutputTokens != nil {
			t.Error("should not init maps on empty paneID")
		}
	})

	t.Run("empty current output returns early", func(t *testing.T) {
		t.Parallel()
		m := &Model{}
		m.recordCostOutputDelta("p1", "gpt-4", "old", "")
		if m.costOutputTokens != nil {
			t.Error("should not init maps on empty currentOutput")
		}
	})

	t.Run("nil maps initialized on valid input", func(t *testing.T) {
		t.Parallel()
		m := &Model{}
		m.recordCostOutputDelta("p1", "gpt-4", "", "some new output here with enough tokens to register")
		if m.costOutputTokens == nil {
			t.Fatal("costOutputTokens should be initialized")
		}
		if m.costModels == nil {
			t.Fatal("costModels should be initialized")
		}
	})

	t.Run("identical output no delta", func(t *testing.T) {
		t.Parallel()
		m := &Model{}
		m.recordCostOutputDelta("p1", "gpt-4", "same\ntext\n", "same\ntext\n")
		if m.costOutputTokens != nil && m.costOutputTokens["p1"] > 0 {
			t.Error("should not record tokens for identical output")
		}
	})

	t.Run("new output records tokens and model", func(t *testing.T) {
		t.Parallel()
		m := &Model{
			costOutputTokens: make(map[string]int),
			costModels:       make(map[string]string),
		}
		m.recordCostOutputDelta("p1", "claude-opus", "line1\nline2\n", "line2\nline3\nline4 with more text for tokens\n")
		if m.costOutputTokens["p1"] <= 0 {
			t.Error("should have recorded positive tokens for output delta")
		}
		if m.costModels["p1"] != "claude-opus" {
			t.Errorf("costModels[p1] = %q, want %q", m.costModels["p1"], "claude-opus")
		}
	})

	t.Run("empty model name not stored", func(t *testing.T) {
		t.Parallel()
		m := &Model{
			costOutputTokens: make(map[string]int),
			costModels:       make(map[string]string),
		}
		m.recordCostOutputDelta("p2", "", "old\n", "old\nnew text with tokens\n")
		if _, ok := m.costModels["p2"]; ok {
			t.Error("should not store empty model name")
		}
	})

	t.Run("tokens accumulate across calls", func(t *testing.T) {
		t.Parallel()
		m := &Model{
			costOutputTokens: make(map[string]int),
			costModels:       make(map[string]string),
		}
		m.recordCostOutputDelta("p3", "gpt-4", "", "first batch of output text here\n")
		first := m.costOutputTokens["p3"]

		m.recordCostOutputDelta("p3", "gpt-4", "first batch of output text here\n", "first batch of output text here\nsecond batch of output\n")
		second := m.costOutputTokens["p3"]

		if second <= first {
			t.Errorf("tokens should accumulate: first=%d, second=%d", first, second)
		}
	})
}

// ---------------------------------------------------------------------------
// renderHealthBadge — Model method, value receiver
// ---------------------------------------------------------------------------

func TestRenderHealthBadge(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	tests := []struct {
		name         string
		healthStatus string
		wantEmpty    bool
		wantContains string
	}{
		{"empty status", "", true, ""},
		{"unknown status", "unknown", true, ""},
		{"unavailable", "unavailable", true, ""},
		{"unrecognized status", "bogus", true, ""},
		{"ok status", "ok", false, "healthy"},
		{"warning status", "warning", false, "drift"},
		{"critical status", "critical", false, "critical"},
		{"no baseline", "no_baseline", false, "no baseline"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := Model{theme: th, healthStatus: tc.healthStatus}
			got := m.renderHealthBadge()
			if tc.wantEmpty && got != "" {
				t.Errorf("renderHealthBadge() = %q, want empty", got)
			}
			if !tc.wantEmpty && got == "" {
				t.Error("renderHealthBadge() returned empty, want non-empty")
			}
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Errorf("renderHealthBadge() = %q, missing %q", got, tc.wantContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// renderScanBadge — Model method, value receiver
// ---------------------------------------------------------------------------

func TestRenderScanBadge(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	tests := []struct {
		name         string
		scanStatus   string
		scanTotals   scanner.ScanTotals
		wantEmpty    bool
		wantContains string
	}{
		{"empty status", "", scanner.ScanTotals{}, true, ""},
		{"unavailable", "unavailable", scanner.ScanTotals{}, true, ""},
		{"unrecognized", "bogus", scanner.ScanTotals{}, true, ""},
		{"clean no findings", "clean", scanner.ScanTotals{}, false, "scan clean"},
		{"clean with findings", "clean", scanner.ScanTotals{Critical: 1, Warning: 2, Info: 3}, false, "scan 1/2/3"},
		{"warning", "warning", scanner.ScanTotals{Warning: 5}, false, "scan 5 warn"},
		{"critical", "critical", scanner.ScanTotals{Critical: 3}, false, "scan 3 crit"},
		{"error", "error", scanner.ScanTotals{}, false, "scan error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := Model{theme: th, scanStatus: tc.scanStatus, scanTotals: tc.scanTotals}
			got := m.renderScanBadge()
			if tc.wantEmpty && got != "" {
				t.Errorf("renderScanBadge() = %q, want empty", got)
			}
			if !tc.wantEmpty && got == "" {
				t.Error("renderScanBadge() returned empty, want non-empty")
			}
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Errorf("renderScanBadge() = %q, missing %q", got, tc.wantContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// renderAgentMailBadge — Model method, value receiver
// ---------------------------------------------------------------------------

func TestRenderAgentMailBadge(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	tests := []struct {
		name         string
		available    bool
		connected    bool
		locks        int
		wantEmpty    bool
		wantContains string
	}{
		{"not available", false, false, 0, true, ""},
		{"available connected no locks", true, true, 0, false, "mail"},
		{"available connected with locks", true, true, 5, false, "5 locks"},
		{"available disconnected", true, false, 0, false, "offline"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := Model{
				theme:              th,
				agentMailAvailable: tc.available,
				agentMailConnected: tc.connected,
				agentMailLocks:     tc.locks,
			}
			got := m.renderAgentMailBadge()
			if tc.wantEmpty && got != "" {
				t.Errorf("renderAgentMailBadge() = %q, want empty", got)
			}
			if !tc.wantEmpty && got == "" {
				t.Error("renderAgentMailBadge() returned empty, want non-empty")
			}
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Errorf("renderAgentMailBadge() = %q, missing %q", got, tc.wantContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// renderCheckpointBadge — Model method, value receiver
// ---------------------------------------------------------------------------

func TestRenderCheckpointBadge(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	tests := []struct {
		name         string
		count        int
		status       string
		wantEmpty    bool
		wantContains string
	}{
		{"zero count", 0, "recent", true, ""},
		{"empty status", 3, "", true, ""},
		{"none status", 3, "none", true, ""},
		{"unrecognized status", 3, "bogus", true, ""},
		{"recent", 2, "recent", false, "2 ckpt"},
		{"stale", 5, "stale", false, "5 stale"},
		{"old", 1, "old", false, "1 old"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := Model{
				theme:            th,
				checkpointCount:  tc.count,
				checkpointStatus: tc.status,
			}
			got := m.renderCheckpointBadge()
			if tc.wantEmpty && got != "" {
				t.Errorf("renderCheckpointBadge() = %q, want empty", got)
			}
			if !tc.wantEmpty && got == "" {
				t.Error("renderCheckpointBadge() returned empty, want non-empty")
			}
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Errorf("renderCheckpointBadge() = %q, missing %q", got, tc.wantContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// renderDCGBadge — Model method, value receiver
// ---------------------------------------------------------------------------

func TestRenderDCGBadge(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	tests := []struct {
		name         string
		enabled      bool
		available    bool
		blocked      int
		wantEmpty    bool
		wantContains string
	}{
		{"not enabled", false, false, 0, true, ""},
		{"enabled not available", true, false, 0, false, "DCG missing"},
		{"enabled available no blocks", true, true, 0, false, "DCG"},
		{"enabled available with blocks", true, true, 3, false, "DCG 3 blocked"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := Model{
				theme:        th,
				dcgEnabled:   tc.enabled,
				dcgAvailable: tc.available,
				dcgBlocked:   tc.blocked,
			}
			got := m.renderDCGBadge()
			if tc.wantEmpty && got != "" {
				t.Errorf("renderDCGBadge() = %q, want empty", got)
			}
			if !tc.wantEmpty && got == "" {
				t.Error("renderDCGBadge() returned empty, want non-empty")
			}
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Errorf("renderDCGBadge() = %q, missing %q", got, tc.wantContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// renderRateLimitAlert — Model method, value receiver
// ---------------------------------------------------------------------------

func TestRenderRateLimitAlert(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	t.Run("no rate limited panes", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme: th,
			panes: []tmux.Pane{
				{Index: 1, Type: tmux.AgentClaude},
				{Index: 2, Type: tmux.AgentCodex},
			},
			paneStatus: map[int]PaneStatus{
				1: {State: "working"},
				2: {State: "idle"},
			},
		}
		got := m.renderRateLimitAlert()
		if got != "" {
			t.Errorf("expected empty for no rate-limited panes, got %q", got)
		}
	})

	t.Run("single rate limited pane", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:   th,
			session: "myproj",
			width:   120,
			panes: []tmux.Pane{
				{Index: 1, Type: tmux.AgentClaude},
				{Index: 2, Type: tmux.AgentCodex},
			},
			paneStatus: map[int]PaneStatus{
				1: {State: "rate_limited"},
				2: {State: "working"},
			},
		}
		got := m.renderRateLimitAlert()
		if got == "" {
			t.Fatal("expected non-empty alert for rate-limited pane")
		}
		if !strings.Contains(got, "Rate limit") {
			t.Errorf("expected 'Rate limit' in alert, got %q", got)
		}
		if !strings.Contains(got, "pane 1") {
			t.Errorf("expected 'pane 1' in alert, got %q", got)
		}
	})

	t.Run("multiple rate limited panes", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:   th,
			session: "myproj",
			width:   120,
			panes: []tmux.Pane{
				{Index: 1, Type: tmux.AgentClaude},
				{Index: 2, Type: tmux.AgentCodex},
				{Index: 3, Type: tmux.AgentGemini},
			},
			paneStatus: map[int]PaneStatus{
				1: {State: "rate_limited"},
				2: {State: "rate_limited"},
				3: {State: "working"},
			},
		}
		got := m.renderRateLimitAlert()
		if got == "" {
			t.Fatal("expected non-empty alert for multiple rate-limited panes")
		}
		if !strings.Contains(got, "panes") {
			t.Errorf("expected 'panes' (plural) in alert, got %q", got)
		}
	})

	t.Run("empty panes list", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th}
		got := m.renderRateLimitAlert()
		if got != "" {
			t.Errorf("expected empty for no panes, got %q", got)
		}
	})

	t.Run("pane status missing", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:      th,
			panes:      []tmux.Pane{{Index: 1, Type: tmux.AgentClaude}},
			paneStatus: map[int]PaneStatus{},
		}
		got := m.renderRateLimitAlert()
		if got != "" {
			t.Errorf("expected empty when pane status missing, got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// tierLabel — pure function
// ---------------------------------------------------------------------------

func TestTierLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tier layout.Tier
		want string
	}{
		{"narrow", layout.TierNarrow, "narrow"},
		{"split", layout.TierSplit, "split"},
		{"wide", layout.TierWide, "wide"},
		{"ultra", layout.TierUltra, "ultra"},
		{"mega", layout.TierMega, "mega"},
		{"unknown tier", layout.Tier(99), "tier-99"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tierLabel(tc.tier)
			if got != tc.want {
				t.Errorf("tierLabel(%d) = %q, want %q", tc.tier, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// normalizedHelpVerbosity — pure function
// ---------------------------------------------------------------------------

func TestNormalizeHelpVerbosity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", "full"},
		{"minimal", "minimal", "minimal"},
		{"MINIMAL uppercase", "MINIMAL", "minimal"},
		{"minimal with spaces", "  minimal  ", "minimal"},
		{"full", "full", "full"},
		{"unknown", "detailed", "full"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizedHelpVerbosity(tc.input)
			if got != tc.want {
				t.Errorf("normalizedHelpVerbosity(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// renderDiagnosticsBar — Model method, value receiver
// ---------------------------------------------------------------------------

func TestRenderDiagnosticsBar_BranchCoverage(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	t.Run("default ok state", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th}
		got := m.renderDiagnosticsBar(80)
		if !strings.Contains(got, "diag") {
			t.Error("expected 'diag' label in diagnostics bar")
		}
	})

	t.Run("fetching session state", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:           th,
			fetchingSession: true,
			lastPaneFetch:   time.Now().Add(-500 * time.Millisecond),
		}
		got := m.renderDiagnosticsBar(80)
		if !strings.Contains(got, "fetching") {
			t.Error("expected 'fetching' in session part")
		}
	})

	t.Run("session fetch latency", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:               th,
			sessionFetchLatency: 250 * time.Millisecond,
		}
		got := m.renderDiagnosticsBar(80)
		if !strings.Contains(got, "250ms") {
			t.Errorf("expected '250ms' latency, got %q", got)
		}
	})

	t.Run("session error overrides latency", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:               th,
			sessionFetchLatency: 100 * time.Millisecond,
			err:                 errors.New("connection failed"),
		}
		got := m.renderDiagnosticsBar(80)
		if !strings.Contains(got, "error") {
			t.Error("expected 'error' in session part when err is set")
		}
	})

	t.Run("fetching context state", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:            th,
			fetchingContext:  true,
			lastContextFetch: time.Now().Add(-200 * time.Millisecond),
		}
		got := m.renderDiagnosticsBar(80)
		if !strings.Contains(got, "fetching") {
			t.Error("expected 'fetching' in context/status part")
		}
	})

	t.Run("status fetch latency", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:              th,
			statusFetchLatency: 150 * time.Millisecond,
		}
		got := m.renderDiagnosticsBar(80)
		if !strings.Contains(got, "150ms") {
			t.Errorf("expected '150ms' in status part, got %q", got)
		}
	})

	t.Run("status fetch error overrides latency", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:              th,
			statusFetchLatency: 100 * time.Millisecond,
			statusFetchErr:     errors.New("timeout"),
		}
		got := m.renderDiagnosticsBar(80)
		if !strings.Contains(got, "error") {
			t.Error("expected 'error' in status part when statusFetchErr is set")
		}
	})

	t.Run("wide width shows age section", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th}
		m.lastUpdated[refreshSession] = time.Now().Add(-30 * time.Second)
		got := m.renderDiagnosticsBar(120)
		if !strings.Contains(got, "age") {
			t.Error("expected 'age' section at width >= 120")
		}
		if !strings.Contains(got, "panes") {
			t.Error("expected 'panes' age label")
		}
	})

	t.Run("narrow width hides age", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th}
		got := m.renderDiagnosticsBar(100)
		if strings.Contains(got, "age") {
			t.Error("should not show age section at width < 120")
		}
	})

	t.Run("age with zero timestamps shows n/a", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th}
		got := m.renderDiagnosticsBar(120)
		if !strings.Contains(got, "n/a") {
			t.Error("expected 'n/a' for zero timestamps")
		}
	})
}

// ---------------------------------------------------------------------------
// renderStatsBar — Model method, value receiver
// ---------------------------------------------------------------------------

func TestRenderStatsBar(t *testing.T) {
	t.Parallel()

	th := theme.Current()
	ic := icons.Current()

	t.Run("minimal with no agents", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th, icons: ic}
		got := m.renderStatsBar()
		if !strings.Contains(got, "0 panes") {
			t.Errorf("expected '0 panes', got %q", got)
		}
	})

	t.Run("with claude agents", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th, icons: ic, claudeCount: 3}
		got := m.renderStatsBar()
		if !strings.Contains(got, "3") {
			t.Error("expected claude count '3' in stats bar")
		}
	})

	t.Run("with codex agents", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th, icons: ic, codexCount: 2}
		got := m.renderStatsBar()
		if !strings.Contains(got, "2") {
			t.Error("expected codex count '2' in stats bar")
		}
	})

	t.Run("with gemini agents", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th, icons: ic, geminiCount: 1}
		got := m.renderStatsBar()
		if !strings.Contains(got, "1") {
			t.Error("expected gemini count '1' in stats bar")
		}
	})

	t.Run("with user panes", func(t *testing.T) {
		t.Parallel()
		m := Model{theme: th, icons: ic, userCount: 1}
		got := m.renderStatsBar()
		if !strings.Contains(got, "1") {
			t.Error("expected user count '1' in stats bar")
		}
	})

	t.Run("with all badges", func(t *testing.T) {
		t.Parallel()
		m := Model{
			theme:              th,
			icons:              ic,
			claudeCount:        2,
			codexCount:         1,
			geminiCount:        1,
			userCount:          1,
			healthStatus:       "ok",
			scanStatus:         "clean",
			agentMailAvailable: true,
			agentMailConnected: true,
			checkpointCount:    3,
			checkpointStatus:   "recent",
			dcgEnabled:         true,
			dcgAvailable:       true,
		}
		got := m.renderStatsBar()
		if got == "" {
			t.Fatal("expected non-empty stats bar with all badges")
		}
		if !strings.Contains(got, "healthy") {
			t.Error("expected health badge in stats bar")
		}
	})
}
