package panels

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// =============================================================================
// Panel rendering integration tests
// =============================================================================

func TestRanoNetworkPanelViewDisabled(t *testing.T) {
	panel := NewRanoNetworkPanel()
	panel.SetSize(60, 12)
	panel.SetData(RanoNetworkPanelData{
		Loaded:  true,
		Enabled: false,
	})

	out := status.StripANSI(panel.View())
	if !strings.Contains(out, "rano disabled") {
		t.Fatalf("expected disabled state, got:\n%s", out)
	}
}

func TestRanoNetworkPanelViewWithRowsExpanded(t *testing.T) {
	panel := NewRanoNetworkPanel()
	panel.SetSize(80, 16)
	panel.SetData(RanoNetworkPanelData{
		Loaded:       true,
		Enabled:      true,
		Available:    true,
		Version:      "0.1.0",
		PollInterval: 1 * time.Second,
		Rows: []RanoNetworkRow{
			{
				Label:        "proj__cc_1",
				AgentType:    "cc",
				RequestCount: 3,
				BytesOut:     45 * 1024,
				BytesIn:      120 * 1024,
				LastRequest:  time.Now().Add(-100 * time.Millisecond),
			},
			{
				Label:        "proj__cod_1",
				AgentType:    "cod",
				RequestCount: 1,
				BytesOut:     10 * 1024,
				BytesIn:      50 * 1024,
				LastRequest:  time.Now().Add(-10 * time.Second),
			},
		},
		TotalRequests: 4,
		TotalBytesOut: 55 * 1024,
		TotalBytesIn:  170 * 1024,
	})

	out := status.StripANSI(panel.View())
	for _, want := range []string{
		"Network Activity",
		"proj__cc_1",
		"proj__cod_1",
		"Total:",
		"By provider:",
		"anthropic:",
		"openai:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestRanoNetworkPanelViewNotAvailable(t *testing.T) {
	panel := NewRanoNetworkPanel()
	panel.SetSize(60, 12)
	panel.SetData(RanoNetworkPanelData{
		Loaded:    true,
		Enabled:   true,
		Available: false,
	})

	out := status.StripANSI(panel.View())
	if !strings.Contains(out, "rano not available") {
		t.Fatalf("expected not-available state, got:\n%s", out)
	}
}

func TestRanoNetworkPanelViewError(t *testing.T) {
	panel := NewRanoNetworkPanel()
	panel.SetSize(60, 12)
	panel.SetData(RanoNetworkPanelData{
		Loaded:  true,
		Enabled: true,
		Error:   errors.New("connection refused"),
	})

	out := status.StripANSI(panel.View())
	if !strings.Contains(out, "connection refused") {
		t.Fatalf("expected error message, got:\n%s", out)
	}
}

func TestRanoNetworkPanelViewNoTraffic(t *testing.T) {
	panel := NewRanoNetworkPanel()
	panel.SetSize(60, 12)
	panel.SetData(RanoNetworkPanelData{
		Loaded:    true,
		Enabled:   true,
		Available: true,
		Rows:      nil,
	})

	out := status.StripANSI(panel.View())
	if !strings.Contains(out, "No agent traffic") {
		t.Fatalf("expected no-traffic state, got:\n%s", out)
	}
}

func TestRanoNetworkPanelViewCompact(t *testing.T) {
	panel := NewRanoNetworkPanel()
	panel.SetSize(80, 10) // h < 14, compact mode
	panel.SetData(RanoNetworkPanelData{
		Loaded:       true,
		Enabled:      true,
		Available:    true,
		PollInterval: 1 * time.Second,
		Rows: []RanoNetworkRow{
			{
				Label:        "proj__cc_1",
				AgentType:    "cc",
				RequestCount: 5,
				BytesOut:     100 * 1024,
				BytesIn:      200 * 1024,
				LastRequest:  time.Now(),
			},
		},
		TotalRequests: 5,
		TotalBytesOut: 100 * 1024,
		TotalBytesIn:  200 * 1024,
	})

	out := status.StripANSI(panel.View())
	if !strings.Contains(out, "proj__cc_1") {
		t.Fatalf("expected agent row, got:\n%s", out)
	}
	// Compact mode should NOT show totals or provider breakdown
	if strings.Contains(out, "Total:") {
		t.Fatalf("compact mode should not show totals, got:\n%s", out)
	}
}

func TestRanoNetworkPanelViewZeroSize(t *testing.T) {
	panel := NewRanoNetworkPanel()
	panel.SetSize(0, 0)
	panel.SetData(RanoNetworkPanelData{Loaded: true})

	out := panel.View()
	if out != "" {
		t.Fatalf("expected empty string for zero size, got: %q", out)
	}
}

func TestRanoNetworkPanelHasData(t *testing.T) {
	panel := NewRanoNetworkPanel()

	if panel.HasData() {
		t.Fatal("HasData() should be false before SetData")
	}

	panel.SetData(RanoNetworkPanelData{Loaded: true})
	if !panel.HasData() {
		t.Fatal("HasData() should be true after SetData with Loaded=true")
	}

	panel2 := NewRanoNetworkPanel()
	panel2.SetData(RanoNetworkPanelData{Error: errors.New("test")})
	if !panel2.HasData() {
		t.Fatal("HasData() should be true when Error is set")
	}
}

func TestRanoNetworkPanelViewWithVersion(t *testing.T) {
	panel := NewRanoNetworkPanel()
	panel.SetSize(80, 16)
	panel.SetData(RanoNetworkPanelData{
		Loaded:    true,
		Enabled:   true,
		Available: true,
		Version:   "1.2.3",
		Rows: []RanoNetworkRow{
			{
				Label:        "test_agent",
				AgentType:    "cc",
				RequestCount: 1,
				BytesOut:     1024,
				BytesIn:      2048,
				LastRequest:  time.Now(),
			},
		},
		TotalRequests: 1,
		TotalBytesOut: 1024,
		TotalBytesIn:  2048,
	})

	out := status.StripANSI(panel.View())
	if !strings.Contains(out, "1.2.3") {
		t.Fatalf("expected version in output, got:\n%s", out)
	}
}

// =============================================================================
// Pure helper function tests
// =============================================================================

func TestProviderFromAgentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"cc", "anthropic"},
		{"claude", "anthropic"},
		{"claude-code", "anthropic"},
		{"claude_code", "anthropic"},
		{"claudecode", "anthropic"},
		{"CC", "anthropic"},
		{"cod", "openai"},
		{"codex", "openai"},
		{"openai-codex", "openai"},
		{"COD", "openai"},
		{"gmi", "google"},
		{"gemini", "google"},
		{"google-gemini", "google"},
		{"google_gemini", "google"},
		{"GMI", "google"},
		{"cursor", "unknown"},
		{"windsurf", "unknown"},
		{"", "unknown"},
		{"  cc  ", "anthropic"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := providerFromAgentType(tc.input)
			if got != tc.want {
				t.Errorf("providerFromAgentType(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestRenderActivity(t *testing.T) {
	t.Parallel()
	poll := 1 * time.Second

	tests := []struct {
		name string
		last time.Time
		want string
	}{
		{"zero time", time.Time{}, "(idle)"},
		{"just now", time.Now(), "▲▲▲"},
		{"3 seconds ago", time.Now().Add(-3 * time.Second), "▲▲"},
		{"20 seconds ago", time.Now().Add(-20 * time.Second), "▲"},
		{"5 minutes ago", time.Now().Add(-5 * time.Minute), "(idle)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := renderActivity(tc.last, poll)
			if got != tc.want {
				t.Errorf("renderActivity(%v, %v) = %q; want %q", tc.last, poll, got, tc.want)
			}
		})
	}
}

func TestRenderActivity_ZeroPollInterval(t *testing.T) {
	t.Parallel()
	// Zero poll interval should default to 1s
	got := renderActivity(time.Now(), 0)
	if got != "▲▲▲" {
		t.Errorf("renderActivity(now, 0) = %q; want %q", got, "▲▲▲")
	}
}

func TestFormatBytesShort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input int64
		want  string
	}{
		{0, "0B"},
		{500, "500B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{10240, "10KB"},
		{1048576, "1.0MB"},
		{10485760, "10MB"},
		{1073741824, "1.0GB"},
		{1099511627776, "1.0TB"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			got := formatBytesShort(tc.input)
			if got != tc.want {
				t.Errorf("formatBytesShort(%d) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestRenderRanoTable_EmptyRows(t *testing.T) {
	t.Parallel()
	out := renderRanoTable(defaultTheme(), 60, nil, time.Second)
	// Should have header but no data rows
	if !strings.Contains(out, "Agent") {
		t.Fatalf("expected table header, got: %q", out)
	}
}

func TestRenderRanoTable_ZeroWidth(t *testing.T) {
	t.Parallel()
	out := renderRanoTable(defaultTheme(), 0, nil, time.Second)
	if out != "" {
		t.Fatalf("expected empty string for zero width, got: %q", out)
	}
}

func TestRenderRanoTable_WithRows(t *testing.T) {
	rows := []RanoNetworkRow{
		{
			Label:        "test__cc_1",
			AgentType:    "cc",
			RequestCount: 10,
			BytesOut:     5120,
			BytesIn:      10240,
			LastRequest:  time.Now(),
		},
	}
	out := status.StripANSI(renderRanoTable(defaultTheme(), 70, rows, time.Second))
	if !strings.Contains(out, "test__cc_1") {
		t.Fatalf("expected agent label, got:\n%s", out)
	}
	if !strings.Contains(out, "10") {
		t.Fatalf("expected request count, got:\n%s", out)
	}
}

func TestRenderRanoTable_UnknownLabel(t *testing.T) {
	rows := []RanoNetworkRow{
		{
			Label:        "",
			AgentType:    "cc",
			RequestCount: 1,
		},
	}
	out := status.StripANSI(renderRanoTable(defaultTheme(), 70, rows, time.Second))
	if !strings.Contains(out, "(unknown)") {
		t.Fatalf("expected (unknown) for empty label, got:\n%s", out)
	}
}

func TestRenderRanoProviderBreakdown(t *testing.T) {
	rows := []RanoNetworkRow{
		{AgentType: "cc", RequestCount: 5, BytesOut: 1024},
		{AgentType: "cod", RequestCount: 3, BytesOut: 2048},
		{AgentType: "gmi", RequestCount: 2, BytesOut: 512},
	}
	out := status.StripANSI(renderRanoProviderBreakdown(defaultTheme(), 120, rows))
	for _, want := range []string{"anthropic:", "openai:", "google:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in breakdown, got:\n%s", want, out)
		}
	}
}

func TestRenderRanoProviderBreakdown_EmptyRows(t *testing.T) {
	out := renderRanoProviderBreakdown(defaultTheme(), 80, nil)
	if out != "" {
		t.Fatalf("expected empty breakdown for no rows, got: %q", out)
	}
}

func TestRenderRanoProviderBreakdown_AllZero(t *testing.T) {
	rows := []RanoNetworkRow{
		{AgentType: "cc", RequestCount: 0, BytesOut: 0, BytesIn: 0},
	}
	out := renderRanoProviderBreakdown(defaultTheme(), 80, rows)
	if out != "" {
		t.Fatalf("expected empty breakdown for zero-traffic rows, got: %q", out)
	}
}

func TestTruncateWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		width int
		check func(string) bool
	}{
		{"empty", "", 10, func(s string) bool { return s == "" }},
		{"fits", "hello", 10, func(s string) bool { return s == "hello" }},
		{"zero width", "hello", 0, func(s string) bool { return s == "" }},
		{"narrow", "hello", 3, func(s string) bool { return len(s) <= 3 }},
		{"exact", "hello", 5, func(s string) bool { return s == "hello" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateWidth(tc.input, tc.width)
			if !tc.check(got) {
				t.Errorf("truncateWidth(%q, %d) = %q", tc.input, tc.width, got)
			}
		})
	}
}

// =============================================================================
// Data flow integration tests (simulating injected adapter data)
// =============================================================================

func TestRanoNetworkPanelDataFlow_MultiAgent(t *testing.T) {
	// Simulate what fetchRanoNetworkStats would produce with a real adapter:
	// two Claude agents, one Codex agent, data aggregated by pane.
	panel := NewRanoNetworkPanel()
	panel.SetSize(100, 20)

	data := RanoNetworkPanelData{
		Loaded:       true,
		Enabled:      true,
		Available:    true,
		Version:      "0.3.0",
		PollInterval: 1 * time.Second,
		Rows: []RanoNetworkRow{
			{
				Label:        "swarm__cc_1",
				AgentType:    "cc",
				RequestCount: 15,
				BytesOut:     150 * 1024,
				BytesIn:      300 * 1024,
				LastRequest:  time.Now().Add(-200 * time.Millisecond),
			},
			{
				Label:        "swarm__cc_2",
				AgentType:    "cc",
				RequestCount: 10,
				BytesOut:     100 * 1024,
				BytesIn:      200 * 1024,
				LastRequest:  time.Now().Add(-2 * time.Second),
			},
			{
				Label:        "swarm__cod_1",
				AgentType:    "cod",
				RequestCount: 7,
				BytesOut:     70 * 1024,
				BytesIn:      140 * 1024,
				LastRequest:  time.Now().Add(-30 * time.Second),
			},
		},
		TotalRequests: 32,
		TotalBytesOut: 320 * 1024,
		TotalBytesIn:  640 * 1024,
	}

	panel.SetData(data)
	out := status.StripANSI(panel.View())

	// All agents present
	for _, label := range []string{"swarm__cc_1", "swarm__cc_2", "swarm__cod_1"} {
		if !strings.Contains(out, label) {
			t.Errorf("expected %q in output, got:\n%s", label, out)
		}
	}

	// Provider breakdown
	if !strings.Contains(out, "anthropic:") {
		t.Errorf("expected anthropic provider, got:\n%s", out)
	}
	if !strings.Contains(out, "openai:") {
		t.Errorf("expected openai provider, got:\n%s", out)
	}

	// Totals
	if !strings.Contains(out, "32 req") {
		t.Errorf("expected total requests, got:\n%s", out)
	}
}

func TestRanoNetworkPanelDataFlow_GeminiOnly(t *testing.T) {
	panel := NewRanoNetworkPanel()
	panel.SetSize(80, 16)

	data := RanoNetworkPanelData{
		Loaded:       true,
		Enabled:      true,
		Available:    true,
		PollInterval: 1 * time.Second,
		Rows: []RanoNetworkRow{
			{
				Label:        "mono__gmi_1",
				AgentType:    "gmi",
				RequestCount: 20,
				BytesOut:     2 * 1024 * 1024,
				BytesIn:      4 * 1024 * 1024,
				LastRequest:  time.Now(),
			},
		},
		TotalRequests: 20,
		TotalBytesOut: 2 * 1024 * 1024,
		TotalBytesIn:  4 * 1024 * 1024,
	}

	panel.SetData(data)
	out := status.StripANSI(panel.View())

	if !strings.Contains(out, "mono__gmi_1") {
		t.Errorf("expected gemini agent, got:\n%s", out)
	}
	if !strings.Contains(out, "google:") {
		t.Errorf("expected google provider, got:\n%s", out)
	}
	// Should NOT have anthropic or openai since there are no such agents
	if strings.Contains(out, "anthropic:") {
		t.Errorf("unexpected anthropic provider for gemini-only panel, got:\n%s", out)
	}
}

// defaultTheme returns the current theme for test use.
func defaultTheme() theme.Theme {
	return theme.Current()
}
