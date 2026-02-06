package export

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// Test helpers

func createTestEvents() []state.AgentEvent {
	now := time.Now()
	return []state.AgentEvent{
		{
			AgentID:   "cc_1",
			AgentType: state.AgentTypeClaude,
			SessionID: "testproject",
			State:     state.TimelineIdle,
			Timestamp: now.Add(-30 * time.Minute),
		},
		{
			AgentID:   "cc_1",
			AgentType: state.AgentTypeClaude,
			SessionID: "testproject",
			State:     state.TimelineWorking,
			Timestamp: now.Add(-25 * time.Minute),
		},
		{
			AgentID:   "cc_1",
			AgentType: state.AgentTypeClaude,
			SessionID: "testproject",
			State:     state.TimelineWaiting,
			Timestamp: now.Add(-10 * time.Minute),
		},
		{
			AgentID:   "cc_1",
			AgentType: state.AgentTypeClaude,
			SessionID: "testproject",
			State:     state.TimelineWorking,
			Timestamp: now.Add(-5 * time.Minute),
		},
		{
			AgentID:   "cod_1",
			AgentType: state.AgentTypeCodex,
			SessionID: "testproject",
			State:     state.TimelineIdle,
			Timestamp: now.Add(-30 * time.Minute),
		},
		{
			AgentID:   "cod_1",
			AgentType: state.AgentTypeCodex,
			SessionID: "testproject",
			State:     state.TimelineWorking,
			Timestamp: now.Add(-20 * time.Minute),
		},
		{
			AgentID:   "cod_1",
			AgentType: state.AgentTypeCodex,
			SessionID: "testproject",
			State:     state.TimelineError,
			Timestamp: now.Add(-8 * time.Minute),
		},
		{
			AgentID:   "gmi_1",
			AgentType: state.AgentTypeGemini,
			SessionID: "testproject",
			State:     state.TimelineWorking,
			Timestamp: now.Add(-15 * time.Minute),
		},
	}
}

// TestDefaultExportOptions verifies default options are sensible
func TestDefaultExportOptions(t *testing.T) {
	opts := DefaultExportOptions()

	t.Logf("Default options: Format=%s Width=%d Scale=%d", opts.Format, opts.Width, opts.Scale)

	if opts.Format != FormatSVG {
		t.Errorf("Expected default format SVG, got %s", opts.Format)
	}
	if opts.Width != 1200 {
		t.Errorf("Expected default width 1200, got %d", opts.Width)
	}
	if opts.Scale != 1 {
		t.Errorf("Expected default scale 1, got %d", opts.Scale)
	}
	if !opts.IncludeLegend {
		t.Error("Expected IncludeLegend to be true by default")
	}
	if !opts.IncludeMetadata {
		t.Error("Expected IncludeMetadata to be true by default")
	}
}

// TestDefaultTheme verifies theme colors are set
func TestDefaultTheme(t *testing.T) {
	theme := DefaultTheme()

	t.Logf("Default theme: Background=%s Working=%s", theme.BackgroundColor, theme.WorkingColor)

	if theme.BackgroundColor == "" {
		t.Error("BackgroundColor should not be empty")
	}
	if theme.WorkingColor == "" {
		t.Error("WorkingColor should not be empty")
	}
	if theme.ErrorColor == "" {
		t.Error("ErrorColor should not be empty")
	}
}

// TestLightTheme verifies light theme differs from default
func TestLightTheme(t *testing.T) {
	dark := DefaultTheme()
	light := LightTheme()

	t.Logf("Dark background: %s, Light background: %s", dark.BackgroundColor, light.BackgroundColor)

	if dark.BackgroundColor == light.BackgroundColor {
		t.Error("Light theme should have different background than dark theme")
	}
}

// TestNewTimelineExporter creates exporter with options
func TestNewTimelineExporter(t *testing.T) {
	opts := ExportOptions{
		Width: 800,
		Scale: 2,
	}

	exporter := NewTimelineExporter(opts)

	if exporter.options.Width != 800 {
		t.Errorf("Expected width 800, got %d", exporter.options.Width)
	}
	if exporter.options.Scale != 2 {
		t.Errorf("Expected scale 2, got %d", exporter.options.Scale)
	}
	// Theme should have been set to default
	if exporter.options.Theme.BackgroundColor == "" {
		t.Error("Theme should have been initialized with defaults")
	}
}

// TestNewTimelineExporter_Defaults applies defaults for zero values
func TestNewTimelineExporter_Defaults(t *testing.T) {
	opts := ExportOptions{} // All zero values

	exporter := NewTimelineExporter(opts)

	if exporter.options.Width != 1200 {
		t.Errorf("Expected default width 1200, got %d", exporter.options.Width)
	}
	if exporter.options.Scale != 1 {
		t.Errorf("Expected default scale 1, got %d", exporter.options.Scale)
	}
}

// TestExportSVG_Basic verifies basic SVG export
func TestExportSVG_Basic(t *testing.T) {
	events := createTestEvents()
	opts := DefaultExportOptions()
	opts.SessionName = "testproject"

	exporter := NewTimelineExporter(opts)
	data, err := exporter.ExportSVG(events)

	if err != nil {
		t.Fatalf("ExportSVG failed: %v", err)
	}

	t.Logf("Generated SVG: %d bytes", len(data))

	svgStr := string(data)

	// Verify SVG structure
	if !strings.HasPrefix(svgStr, "<?xml") {
		t.Error("SVG should start with XML declaration")
	}
	if !strings.Contains(svgStr, "<svg") {
		t.Error("SVG should contain <svg> element")
	}
	if !strings.Contains(svgStr, "</svg>") {
		t.Error("SVG should have closing </svg> tag")
	}

	// Verify content
	if !strings.Contains(svgStr, "cc_1") {
		t.Error("SVG should contain agent ID cc_1")
	}
	if !strings.Contains(svgStr, "cod_1") {
		t.Error("SVG should contain agent ID cod_1")
	}
	if !strings.Contains(svgStr, "testproject") {
		t.Error("SVG should contain session name")
	}
}

// TestExportSVG_EmptyEvents returns error for empty events
func TestExportSVG_EmptyEvents(t *testing.T) {
	opts := DefaultExportOptions()
	exporter := NewTimelineExporter(opts)

	_, err := exporter.ExportSVG([]state.AgentEvent{})

	if err == nil {
		t.Error("Expected error for empty events")
	}
	if !strings.Contains(err.Error(), "no events") {
		t.Errorf("Expected 'no events' error, got: %v", err)
	}
}

// TestExportSVG_LightTheme verifies light theme is applied
func TestExportSVG_LightTheme(t *testing.T) {
	events := createTestEvents()
	opts := DefaultExportOptions()
	opts.Theme = LightTheme()

	exporter := NewTimelineExporter(opts)
	data, err := exporter.ExportSVG(events)

	if err != nil {
		t.Fatalf("ExportSVG failed: %v", err)
	}

	svgStr := string(data)

	// Light theme should have white-ish background
	if !strings.Contains(svgStr, "#ffffff") {
		t.Error("Light theme SVG should contain white background color")
	}
}

// TestExportSVG_NoLegend omits legend when disabled
func TestExportSVG_NoLegend(t *testing.T) {
	events := createTestEvents()
	opts := DefaultExportOptions()
	opts.IncludeLegend = false

	exporter := NewTimelineExporter(opts)
	data, err := exporter.ExportSVG(events)

	if err != nil {
		t.Fatalf("ExportSVG failed: %v", err)
	}

	svgStr := string(data)

	// Legend should not appear
	if strings.Contains(svgStr, "Legend:") {
		t.Error("SVG should not contain legend when IncludeLegend=false")
	}
}

// TestExportSVG_NoMetadata omits metadata when disabled
func TestExportSVG_NoMetadata(t *testing.T) {
	events := createTestEvents()
	opts := DefaultExportOptions()
	opts.IncludeMetadata = false
	opts.SessionName = "testproject"

	exporter := NewTimelineExporter(opts)
	data, err := exporter.ExportSVG(events)

	if err != nil {
		t.Fatalf("ExportSVG failed: %v", err)
	}

	svgStr := string(data)

	// Should still have agent tracks but no title metadata
	if !strings.Contains(svgStr, "cc_1") {
		t.Error("SVG should still contain agent tracks")
	}
}

// TestExportSVG_TimeRange filters events by time
func TestExportSVG_TimeRange(t *testing.T) {
	events := createTestEvents()
	now := time.Now()

	opts := DefaultExportOptions()
	opts.Since = now.Add(-15 * time.Minute) // Only last 15 minutes

	exporter := NewTimelineExporter(opts)
	data, err := exporter.ExportSVG(events)

	if err != nil {
		t.Fatalf("ExportSVG failed: %v", err)
	}

	t.Logf("Time-filtered SVG: %d bytes", len(data))

	// SVG should still be valid
	svgStr := string(data)
	if !strings.Contains(svgStr, "<svg") {
		t.Error("SVG should still be valid with time filter")
	}
}

// TestExportPNG_Basic verifies basic PNG export
func TestExportPNG_Basic(t *testing.T) {
	events := createTestEvents()
	opts := DefaultExportOptions()
	opts.Format = FormatPNG
	opts.SessionName = "testproject"

	exporter := NewTimelineExporter(opts)
	data, err := exporter.ExportPNG(events)

	if err != nil {
		t.Fatalf("ExportPNG failed: %v", err)
	}

	t.Logf("Generated PNG: %d bytes", len(data))

	// Verify PNG structure
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PNG decode failed: %v", err)
	}

	bounds := img.Bounds()
	t.Logf("PNG dimensions: %dx%d", bounds.Dx(), bounds.Dy())

	if bounds.Dx() != 1200 {
		t.Errorf("Expected width 1200, got %d", bounds.Dx())
	}
}

// TestExportPNG_Scale verifies scale multiplier
func TestExportPNG_Scale(t *testing.T) {
	events := createTestEvents()
	opts := DefaultExportOptions()
	opts.Format = FormatPNG
	opts.Width = 600
	opts.Scale = 2

	exporter := NewTimelineExporter(opts)
	data, err := exporter.ExportPNG(events)

	if err != nil {
		t.Fatalf("ExportPNG failed: %v", err)
	}

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PNG decode failed: %v", err)
	}

	bounds := img.Bounds()
	t.Logf("Scaled PNG dimensions: %dx%d", bounds.Dx(), bounds.Dy())

	// Width should be 600 * 2 = 1200
	if bounds.Dx() != 1200 {
		t.Errorf("Expected scaled width 1200, got %d", bounds.Dx())
	}
}

// TestExportPNG_EmptyEvents returns error for empty events
func TestExportPNG_EmptyEvents(t *testing.T) {
	opts := DefaultExportOptions()
	opts.Format = FormatPNG
	exporter := NewTimelineExporter(opts)

	_, err := exporter.ExportPNG([]state.AgentEvent{})

	if err == nil {
		t.Error("Expected error for empty events")
	}
}

// TestParseHexColor parses valid hex colors
func TestParseHexColor(t *testing.T) {
	tests := []struct {
		input string
		wantR uint8
		wantG uint8
		wantB uint8
	}{
		{"#ffffff", 255, 255, 255},
		{"#000000", 0, 0, 0},
		{"#a6e3a1", 166, 227, 161},
		{"ffffff", 255, 255, 255}, // Without # prefix
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			c := parseHexColor(tt.input)
			t.Logf("parseHexColor(%s) = RGBA(%d,%d,%d,%d)", tt.input, c.R, c.G, c.B, c.A)

			if c.R != tt.wantR || c.G != tt.wantG || c.B != tt.wantB {
				t.Errorf("parseHexColor(%s) = (%d,%d,%d), want (%d,%d,%d)",
					tt.input, c.R, c.G, c.B, tt.wantR, tt.wantG, tt.wantB)
			}
			if c.A != 255 {
				t.Errorf("Alpha should be 255, got %d", c.A)
			}
		})
	}
}

// TestParseHexColor_Invalid returns default for invalid input
func TestParseHexColor_Invalid(t *testing.T) {
	c := parseHexColor("invalid")
	t.Logf("parseHexColor(invalid) = RGBA(%d,%d,%d,%d)", c.R, c.G, c.B, c.A)

	// Should return gray fallback
	if c.R != 128 || c.G != 128 || c.B != 128 {
		t.Errorf("Invalid input should return gray, got (%d,%d,%d)", c.R, c.G, c.B)
	}
}

// TestGetAgentType detects agent types from IDs
func TestGetAgentType(t *testing.T) {
	exporter := NewTimelineExporter(DefaultExportOptions())

	tests := []struct {
		agentID  string
		expected string
	}{
		{"cc_1", "claude"},
		{"cc_2", "claude"},
		{"cod_1", "codex"},
		{"cod_3", "codex"},
		{"gmi_1", "gemini"},
		{"gmi_2", "gemini"},
		{"unknown_1", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			result := exporter.getAgentType(tt.agentID)
			t.Logf("getAgentType(%s) = %s", tt.agentID, result)

			if result != tt.expected {
				t.Errorf("getAgentType(%s) = %s, want %s", tt.agentID, result, tt.expected)
			}
		})
	}
}

// TestGetStateColor returns correct colors for states
func TestGetStateColor(t *testing.T) {
	theme := DefaultTheme()
	exporter := NewTimelineExporter(ExportOptions{Theme: theme})

	tests := []struct {
		state    state.TimelineState
		expected string
	}{
		{state.TimelineIdle, theme.IdleColor},
		{state.TimelineWorking, theme.WorkingColor},
		{state.TimelineWaiting, theme.WaitingColor},
		{state.TimelineError, theme.ErrorColor},
		{state.TimelineStopped, theme.StoppedColor},
		{state.TimelineState("unknown-state"), theme.IdleColor}, // default branch
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			result := exporter.getStateColor(tt.state)
			t.Logf("getStateColor(%s) = %s", tt.state, result)

			if result != tt.expected {
				t.Errorf("getStateColor(%s) = %s, want %s", tt.state, result, tt.expected)
			}
		})
	}
}

// TestGetAgentColor returns correct colors for agent types
func TestGetAgentColor(t *testing.T) {
	theme := DefaultTheme()
	exporter := NewTimelineExporter(ExportOptions{Theme: theme})

	tests := []struct {
		agentID  string
		expected string
	}{
		{"cc_1", theme.ClaudeColor},
		{"cod_1", theme.CodexColor},
		{"gmi_1", theme.GeminiColor},
		{"other_1", theme.TextColor}, // default branch
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			result := exporter.getAgentColor(tt.agentID)
			if result != tt.expected {
				t.Errorf("getAgentColor(%s) = %s, want %s", tt.agentID, result, tt.expected)
			}
		})
	}
}

// TestBuildSegments creates segments from events
func TestBuildSegments(t *testing.T) {
	exporter := NewTimelineExporter(DefaultExportOptions())

	now := time.Now()
	events := []state.AgentEvent{
		{
			AgentID:   "cc_1",
			State:     state.TimelineWorking,
			Timestamp: now.Add(-20 * time.Minute),
		},
		{
			AgentID:   "cc_1",
			State:     state.TimelineIdle,
			Timestamp: now.Add(-10 * time.Minute),
		},
	}

	timeStart := now.Add(-30 * time.Minute)
	timeEnd := now
	duration := timeEnd.Sub(timeStart)

	segments := exporter.buildSegments(events, timeStart, timeEnd, duration, 100, 1000)

	t.Logf("Built %d segments", len(segments))
	for i, seg := range segments {
		t.Logf("  Segment %d: State=%s Duration=%v XStart=%.1f Width=%.1f",
			i, seg.State, seg.Duration, seg.XStart, seg.Width)
	}

	// Should have 3 segments: idle (before first event), working, idle
	if len(segments) != 3 {
		t.Errorf("Expected 3 segments, got %d", len(segments))
	}

	// First segment should be idle (before first event)
	if segments[0].State != state.TimelineIdle {
		t.Errorf("First segment should be idle, got %s", segments[0].State)
	}

	// Second segment should be working
	if segments[1].State != state.TimelineWorking {
		t.Errorf("Second segment should be working, got %s", segments[1].State)
	}

	// Third segment should be idle
	if segments[2].State != state.TimelineIdle {
		t.Errorf("Third segment should be idle, got %s", segments[2].State)
	}
}

// TestPrepareData groups events by agent
func TestPrepareData(t *testing.T) {
	events := createTestEvents()
	opts := DefaultExportOptions()
	opts.SessionName = "testproject"

	exporter := NewTimelineExporter(opts)
	data := exporter.prepareData(events)

	t.Logf("Prepared data: %d agents, %d total events", len(data.Agents), data.TotalEvents)
	for _, agent := range data.Agents {
		t.Logf("  Agent %s: %d segments, color=%s", agent.AgentID, len(agent.Segments), agent.Color)
	}

	if len(data.Agents) != 3 {
		t.Errorf("Expected 3 agents, got %d", len(data.Agents))
	}

	if data.TotalEvents != len(events) {
		t.Errorf("Expected %d total events, got %d", len(events), data.TotalEvents)
	}

	if data.SessionName != "testproject" {
		t.Errorf("Expected session name 'testproject', got '%s'", data.SessionName)
	}
}

// BenchmarkExportSVG measures SVG export performance
func BenchmarkExportSVG(b *testing.B) {
	events := createTestEvents()
	opts := DefaultExportOptions()
	exporter := NewTimelineExporter(opts)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := exporter.ExportSVG(events)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExportPNG measures PNG export performance
func BenchmarkExportPNG(b *testing.B) {
	events := createTestEvents()
	opts := DefaultExportOptions()
	opts.Format = FormatPNG
	exporter := NewTimelineExporter(opts)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := exporter.ExportPNG(events)
		if err != nil {
			b.Fatal(err)
		}
	}
}
