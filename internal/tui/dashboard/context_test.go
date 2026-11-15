package dashboard

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestFormatTokenDisplay(t *testing.T) {

	tests := []struct {
		name     string
		used     int
		limit    int
		expected string
	}{
		{
			name:     "small numbers",
			used:     500,
			limit:    1000,
			expected: "500 / 1.0K",
		},
		{
			name:     "thousands",
			used:     142500,
			limit:    200000,
			expected: "142.5K / 200.0K",
		},
		{
			name:     "millions",
			used:     1500000,
			limit:    2000000,
			expected: "1.5M / 2.0M",
		},
		{
			name:     "mixed",
			used:     50000,
			limit:    1000000,
			expected: "50.0K / 1.0M",
		},
		{
			name:     "under 1K",
			used:     100,
			limit:    500,
			expected: "100 / 500",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := formatTokenDisplay(tc.used, tc.limit)
			if result != tc.expected {
				t.Errorf("formatTokenDisplay(%d, %d) = %q, want %q", tc.used, tc.limit, result, tc.expected)
			}
		})
	}
}

func TestPaneStatusRotationFields(t *testing.T) {

	// Test that PaneStatus has the rotation fields
	ps := PaneStatus{
		State:          "running",
		ContextTokens:  150000,
		ContextLimit:   200000,
		ContextPercent: 75.0,
		IsRotating:     true,
	}

	if !ps.IsRotating {
		t.Error("expected IsRotating to be true")
	}

	now := time.Now()
	ps.RotatedAt = &now
	if ps.RotatedAt == nil {
		t.Error("expected RotatedAt to be set")
	}
}

func TestRenderContextBar(t *testing.T) {

	m := New("session", "")
	m.width = 120
	m.height = 30
	m.tier = layout.TierForWidth(m.width)

	tests := []struct {
		name    string
		percent float64
		width   int
	}{
		{
			name:    "low usage",
			percent: 25.0,
			width:   20,
		},
		{
			name:    "medium usage",
			percent: 50.0,
			width:   20,
		},
		{
			name:    "high usage",
			percent: 75.0,
			width:   20,
		},
		{
			name:    "critical usage",
			percent: 90.0,
			width:   20,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := m.renderContextBar(tc.percent, tc.width)
			// Just verify it returns a non-empty string
			if result == "" {
				t.Errorf("renderContextBar(%.1f, %d) returned empty string", tc.percent, tc.width)
			}
		})
	}
}

func TestContextUsagePaletteThresholds(t *testing.T) {
	currentTheme := theme.CatppuccinMocha
	tests := []struct {
		name    string
		percent float64
		want    []string
	}{
		{name: "green below sixty", percent: 59.9, want: []string{string(currentTheme.Green), string(currentTheme.Green)}},
		{name: "yellow at sixty", percent: 60, want: []string{string(currentTheme.Yellow), string(currentTheme.Yellow)}},
		{name: "orange at eighty", percent: 80, want: []string{string(currentTheme.Peach), string(currentTheme.Peach)}},
		{name: "orange at ninety-five", percent: 95, want: []string{string(currentTheme.Peach), string(currentTheme.Peach)}},
		{name: "red above ninety-five", percent: 95.1, want: []string{string(currentTheme.Red), string(currentTheme.Maroon)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contextUsagePalette(tt.percent, currentTheme); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("contextUsagePalette(%v) = %v, want %v", tt.percent, got, tt.want)
			}
		})
	}
}

func TestRenderContextBarNarrowWidth(t *testing.T) {
	m := New("session", "")

	got := status.StripANSI(m.renderContextBar(71, 1))
	if !strings.Contains(got, "71%") {
		t.Fatalf("narrow context bar = %q, want percentage", got)
	}
}

func TestPaneStatusWithContextData(t *testing.T) {

	// Test that PaneStatus correctly stores context data
	ps := PaneStatus{
		State:          "running",
		ContextTokens:  142500,
		ContextLimit:   200000,
		ContextPercent: 71.25,
	}

	if ps.ContextTokens != 142500 {
		t.Errorf("expected ContextTokens=142500, got %d", ps.ContextTokens)
	}
	if ps.ContextLimit != 200000 {
		t.Errorf("expected ContextLimit=200000, got %d", ps.ContextLimit)
	}
	if ps.ContextPercent != 71.25 {
		t.Errorf("expected ContextPercent=71.25, got %f", ps.ContextPercent)
	}

	// Verify formatTokenDisplay works with these values
	display := formatTokenDisplay(ps.ContextTokens, ps.ContextLimit)
	expected := "142.5K / 200.0K"
	if display != expected {
		t.Errorf("formatTokenDisplay(%d, %d) = %q, want %q",
			ps.ContextTokens, ps.ContextLimit, display, expected)
	}
}

func TestPaneStatusRotationState(t *testing.T) {

	// Test rotation in progress
	psRotating := PaneStatus{
		State:      "running",
		IsRotating: true,
	}
	if !psRotating.IsRotating {
		t.Error("expected IsRotating=true")
	}

	// Test recently rotated
	rotatedTime := time.Now().Add(-2 * time.Minute)
	psRotated := PaneStatus{
		State:      "running",
		IsRotating: false,
		RotatedAt:  &rotatedTime,
	}
	if psRotated.IsRotating {
		t.Error("expected IsRotating=false after rotation completes")
	}
	if psRotated.RotatedAt == nil {
		t.Error("expected RotatedAt to be set")
	}
}

func TestResolveCostAndContextModelForPane_MultiHarness(t *testing.T) {
	t.Parallel()

	t.Run("nil config defaults and empty unconfigured harnesses", func(t *testing.T) {
		m := &Model{}

		cases := []struct {
			name        string
			pane        tmux.Pane
			wantCost    string
			wantContext string
		}{
			{
				name:        "claude fallback",
				pane:        tmux.Pane{Type: tmux.AgentType("claude")},
				wantCost:    config.ClaudeEstimationModel,
				wantContext: config.ClaudeEstimationModel,
			},
			{
				name:        "codex fallback",
				pane:        tmux.Pane{Type: tmux.AgentType("codex")},
				wantCost:    "gpt-4",
				wantContext: "gpt-4",
			},
			{
				name:        "gemini fallback",
				pane:        tmux.Pane{Type: tmux.AgentType("gemini")},
				wantCost:    "gemini-2.0-flash",
				wantContext: "gemini-2.0-flash",
			},
			{
				name:        "antigravity nil config fallback",
				pane:        tmux.Pane{Type: tmux.AgentType("antigravity")},
				wantCost:    config.AntigravityRequiredModel,
				wantContext: config.AntigravityRequiredModel,
			},
			{
				name:        "grok nil config",
				pane:        tmux.Pane{Type: tmux.AgentGrok},
				wantCost:    "",
				wantContext: "",
			},
			{
				name:        "omp nil config",
				pane:        tmux.Pane{Type: tmux.AgentOmp},
				wantCost:    "",
				wantContext: "",
			},
			{
				name:        "opencode alias nil config",
				pane:        tmux.Pane{Type: tmux.AgentType("oc")},
				wantCost:    "",
				wantContext: "",
			},
			{
				name:        "ollama nil config",
				pane:        tmux.Pane{Type: tmux.AgentOllama},
				wantCost:    "",
				wantContext: "",
			},
			{
				name:        "cursor context-only model",
				pane:        tmux.Pane{Type: tmux.AgentCursor},
				wantCost:    "",
				wantContext: "cursor",
			},
		}

		for _, tc := range cases {
			if got := m.resolveCostModelForPane(tc.pane); got != tc.wantCost {
				t.Errorf("%s: resolveCostModelForPane() = %q, want %q", tc.name, got, tc.wantCost)
			}
			if got := m.resolveContextModelForPane(tc.pane); got != tc.wantContext {
				t.Errorf("%s: resolveContextModelForPane() = %q, want %q", tc.name, got, tc.wantContext)
			}
		}
	})

	t.Run("configured defaults and antigravity fallback chain", func(t *testing.T) {
		m := &Model{
			cfg: &config.Config{
				Models: config.ModelsConfig{
					DefaultClaude:      "claude-opus-4-6",
					DefaultCodex:       "gpt-5-codex",
					DefaultGemini:      "gemini-3-pro-preview",
					DefaultAntigravity: "gemini-4-argon",
					DefaultGrok:        "grok-4",
					DefaultOmp:         "openrouter/stealth/union-alpha",
					DefaultOpencode:    "anthropic/claude-sonnet-4",
					DefaultOllama:      "llama3",
				},
			},
		}

		cases := []struct {
			agentType tmux.AgentType
			want      string
		}{
			{tmux.AgentClaude, "claude-opus-4-6"},
			{tmux.AgentCodex, "gpt-5-codex"},
			{tmux.AgentGemini, "gemini-3-pro-preview"},
			{tmux.AgentAntigravity, "gemini-4-argon"},
			{tmux.AgentType("antigravity"), "gemini-4-argon"},
			{tmux.AgentGrok, "grok-4"},
			{tmux.AgentType("grok-build"), "grok-4"},
			{tmux.AgentOmp, "openrouter/stealth/union-alpha"},
			{tmux.AgentOpencode, "anthropic/claude-sonnet-4"},
			{tmux.AgentType("oc"), "anthropic/claude-sonnet-4"},
			{tmux.AgentOllama, "llama3"},
		}

		for _, tc := range cases {
			pane := tmux.Pane{Type: tc.agentType}
			if got := m.resolveCostModelForPane(pane); got != tc.want {
				t.Errorf("type %q: resolveCostModelForPane() = %q, want %q", tc.agentType, got, tc.want)
			}
			if got := m.resolveContextModelForPane(pane); got != tc.want {
				t.Errorf("type %q: resolveContextModelForPane() = %q, want %q", tc.agentType, got, tc.want)
			}

			// Pane Variant always takes precedence over config defaults.
			variantPane := tmux.Pane{Type: tc.agentType, Variant: "custom-variant-model"}
			if got := m.resolveCostModelForPane(variantPane); got != "custom-variant-model" {
				t.Errorf("type %q with variant: resolveCostModelForPane() = %q, want custom-variant-model", tc.agentType, got)
			}
			if got := m.resolveContextModelForPane(variantPane); got != "custom-variant-model" {
				t.Errorf("type %q with variant: resolveContextModelForPane() = %q, want custom-variant-model", tc.agentType, got)
			}
		}

		// When DefaultAntigravity is empty, Antigravity falls back to DefaultGemini,
		// and when both are empty, to config.AntigravityRequiredModel.
		m.cfg.Models.DefaultAntigravity = ""
		if got := m.resolveCostModelForPane(tmux.Pane{Type: tmux.AgentAntigravity}); got != "gemini-3-pro-preview" {
			t.Fatalf("antigravity fallback to DefaultGemini = %q, want gemini-3-pro-preview", got)
		}
		m.cfg.Models.DefaultGemini = ""
		if got := m.resolveCostModelForPane(tmux.Pane{Type: tmux.AgentAntigravity}); got != config.AntigravityRequiredModel {
			t.Fatalf("antigravity fallback to AntigravityRequiredModel = %q, want %q", got, config.AntigravityRequiredModel)
		}
	})
}

func TestSessionDataWithOutputMultiHarnessContextAndCostModels(t *testing.T) {
	m := New("session", "")
	m.startupWarmupDone = true
	m.cfg = &config.Config{
		Models: config.ModelsConfig{
			DefaultAntigravity: "Gemini 4 Argon (High)",
			DefaultGrok:        "grok-4",
			DefaultOmp:         "claude-sonnet-4",
			DefaultOpencode:    "gpt-4o",
			DefaultOllama:      "llama3",
		},
	}

	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Title: "session__agy_1", Type: tmux.AgentAntigravity},
		{ID: "%2", Index: 2, Title: "session__grok_1", Type: tmux.AgentGrok},
		{ID: "%3", Index: 3, Title: "session__omp_1", Type: tmux.AgentOmp},
		{ID: "%4", Index: 4, Title: "session__oc_1", Type: tmux.AgentOpencode},
		{ID: "%5", Index: 5, Title: "session__ollama_1", Type: tmux.AgentOllama},
	}
	m.panes = panes

	now := time.Now()
	outputs := make([]PaneOutputData, 0, len(panes))
	rawAgentTypes := []string{"antigravity", "grok", "omp", "oc", "ollama"}
	for i, p := range panes {
		obs := dashboardStatusObservation(now, status.AgentStatus{
			PaneID:    p.ID,
			PaneName:  p.Title,
			AgentType: rawAgentTypes[i],
			State:     status.StateWorking,
			UpdatedAt: now,
		}).Panes[0]
		obs.Pane = p.Ref()
		outputs = append(outputs, PaneOutputData{
			PaneID:       p.ID,
			PaneIndex:    p.Index,
			LastActivity: now,
			Output:       strings.Repeat("multi-harness agent output line\n", 40),
			AgentType:    rawAgentTypes[i],
			Observation:  obs,
		})
	}

	updated, _ := m.Update(SessionDataWithOutputMsg{
		Panes:   panes,
		Outputs: outputs,
	})
	m = updated.(Model)

	wantModels := map[string]string{
		"%1": "Gemini 4 Argon (High)",
		"%2": "grok-4",
		"%3": "claude-sonnet-4",
		"%4": "gpt-4o",
		"%5": "llama3",
	}

	for _, p := range panes {
		want := wantModels[p.ID]
		ps := m.paneStatus[paneStatusKey(p)]
		if ps.ContextModel != want {
			t.Errorf("pane %s (%s) ContextModel = %q, want %q", p.ID, p.Type, ps.ContextModel, want)
		}
		if ps.ContextTokens <= 0 || ps.ContextLimit <= 0 {
			t.Errorf("pane %s (%s) context usage not computed: tokens=%d limit=%d", p.ID, p.Type, ps.ContextTokens, ps.ContextLimit)
		}
		if gotCostModel := m.costModels[p.ID]; gotCostModel != want {
			t.Errorf("pane %s (%s) costModels = %q, want %q", p.ID, p.Type, gotCostModel, want)
		}
	}
}
