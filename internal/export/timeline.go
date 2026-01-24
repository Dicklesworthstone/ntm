// Package export provides functionality for exporting timeline visualizations
// to static image formats like SVG and PNG.
package export

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// ExportFormat represents the output format for timeline export.
type ExportFormat string

const (
	FormatSVG   ExportFormat = "svg"
	FormatPNG   ExportFormat = "png"
	FormatJSONL ExportFormat = "jsonl"
)

// ExportOptions configures the timeline export.
type ExportOptions struct {
	// Format is the output format (svg, png, jsonl).
	Format ExportFormat

	// TimeRange filters events to a specific time window.
	// If Since is zero, uses the first event time.
	// If Until is zero, uses the last event time.
	Since time.Time
	Until time.Time

	// Dimensions for the output image.
	Width  int // Default: 1200
	Height int // Default: auto-calculated based on agent count

	// Scale multiplier for PNG export (1x, 2x, 3x).
	// Ignored for SVG.
	Scale int // Default: 1

	// Theme controls the color scheme.
	Theme ExportTheme

	// IncludeLegend adds a color legend to the export.
	IncludeLegend bool

	// SessionName is included in the export metadata.
	SessionName string

	// IncludeMetadata adds session info to the export.
	IncludeMetadata bool
}

// ExportTheme defines colors for different timeline states.
type ExportTheme struct {
	BackgroundColor string
	TextColor       string
	AxisColor       string
	IdleColor       string
	WorkingColor    string
	WaitingColor    string
	ErrorColor      string
	StoppedColor    string
	ClaudeColor     string
	CodexColor      string
	GeminiColor     string
}

// DefaultTheme returns the default color theme matching the TUI.
func DefaultTheme() ExportTheme {
	return ExportTheme{
		BackgroundColor: "#1e1e2e", // Catppuccin base
		TextColor:       "#cdd6f4", // Text
		AxisColor:       "#6c7086", // Overlay
		IdleColor:       "#6c7086", // Overlay (gray)
		WorkingColor:    "#a6e3a1", // Green
		WaitingColor:    "#f9e2af", // Yellow
		ErrorColor:      "#f38ba8", // Red
		StoppedColor:    "#313244", // Surface0 (dark gray)
		ClaudeColor:     "#cba6f7", // Mauve (Claude purple)
		CodexColor:      "#89dceb", // Sky (Codex blue)
		GeminiColor:     "#94e2d5", // Teal (Gemini)
	}
}

// LightTheme returns a light color theme suitable for print/docs.
func LightTheme() ExportTheme {
	return ExportTheme{
		BackgroundColor: "#ffffff",
		TextColor:       "#1e1e2e",
		AxisColor:       "#6c7086",
		IdleColor:       "#9ca0b0",
		WorkingColor:    "#40a02b",
		WaitingColor:    "#df8e1d",
		ErrorColor:      "#d20f39",
		StoppedColor:    "#ccd0da",
		ClaudeColor:     "#8839ef",
		CodexColor:      "#04a5e5",
		GeminiColor:     "#179299",
	}
}

// DefaultExportOptions returns sensible defaults for export.
func DefaultExportOptions() ExportOptions {
	return ExportOptions{
		Format:          FormatSVG,
		Width:           1200,
		Height:          0, // Auto-calculate
		Scale:           1,
		Theme:           DefaultTheme(),
		IncludeLegend:   true,
		IncludeMetadata: true,
	}
}

// TimelineExporter handles exporting timeline events to various formats.
type TimelineExporter struct {
	options ExportOptions
}

// NewTimelineExporter creates a new exporter with the given options.
func NewTimelineExporter(opts ExportOptions) *TimelineExporter {
	// Apply defaults for missing values
	if opts.Width == 0 {
		opts.Width = 1200
	}
	if opts.Scale == 0 {
		opts.Scale = 1
	}
	if opts.Theme.BackgroundColor == "" {
		opts.Theme = DefaultTheme()
	}
	return &TimelineExporter{options: opts}
}

// ExportSVG exports timeline events to SVG format.
func (e *TimelineExporter) ExportSVG(events []state.AgentEvent) ([]byte, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("no events to export")
	}

	data := e.prepareData(events)
	return e.renderSVG(data)
}

// ExportPNG exports timeline events to PNG format.
func (e *TimelineExporter) ExportPNG(events []state.AgentEvent) ([]byte, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("no events to export")
	}

	data := e.prepareData(events)
	return e.renderPNG(data)
}

// timelineData holds preprocessed data for rendering.
type timelineData struct {
	Agents          []agentTrack
	TimeStart       time.Time
	TimeEnd         time.Time
	Duration        time.Duration
	Width           int
	Height          int
	BarWidth        int
	BarHeight       int
	LeftMargin      int
	TopMargin       int
	BottomMargin    int
	SessionName     string
	ExportTime      time.Time
	TotalEvents     int
	Theme           ExportTheme
	IncludeLegend   bool
	IncludeMetadata bool
}

// agentTrack represents a single agent's timeline.
type agentTrack struct {
	AgentID   string
	AgentType string
	Color     string
	Segments  []timeSegment
	YOffset   int
}

// timeSegment represents a continuous time segment with a single state.
type timeSegment struct {
	State     state.TimelineState
	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration
	XStart    float64 // Pixel position
	XEnd      float64
	Width     float64
	Color     string
}

func (e *TimelineExporter) prepareData(events []state.AgentEvent) *timelineData {
	opts := e.options

	// Determine time range
	timeStart := opts.Since
	timeEnd := opts.Until

	if timeStart.IsZero() && len(events) > 0 {
		timeStart = events[0].Timestamp
		for _, ev := range events {
			if ev.Timestamp.Before(timeStart) {
				timeStart = ev.Timestamp
			}
		}
	}

	if timeEnd.IsZero() && len(events) > 0 {
		timeEnd = events[len(events)-1].Timestamp
		for _, ev := range events {
			if ev.Timestamp.After(timeEnd) {
				timeEnd = ev.Timestamp
			}
		}
	}

	// Add small padding to time range
	duration := timeEnd.Sub(timeStart)
	if duration < time.Minute {
		duration = time.Minute
	}

	// Group events by agent
	agentEvents := make(map[string][]state.AgentEvent)
	for _, ev := range events {
		agentEvents[ev.AgentID] = append(agentEvents[ev.AgentID], ev)
	}

	// Sort events within each agent
	for agentID := range agentEvents {
		sort.Slice(agentEvents[agentID], func(i, j int) bool {
			return agentEvents[agentID][i].Timestamp.Before(agentEvents[agentID][j].Timestamp)
		})
	}

	// Get sorted list of agents
	agentIDs := make([]string, 0, len(agentEvents))
	for id := range agentEvents {
		agentIDs = append(agentIDs, id)
	}
	sort.Strings(agentIDs)

	// Layout calculations
	leftMargin := 120  // Space for agent labels
	topMargin := 60    // Space for title
	bottomMargin := 80 // Space for time axis and legend
	barHeight := 30    // Height of each timeline bar
	barGap := 10       // Gap between bars
	barWidth := opts.Width - leftMargin - 40

	// Calculate height if not specified
	height := opts.Height
	if height == 0 {
		height = topMargin + bottomMargin + (len(agentIDs) * (barHeight + barGap)) + 20
		if opts.IncludeLegend {
			height += 50
		}
	}

	// Build agent tracks
	tracks := make([]agentTrack, 0, len(agentIDs))
	for i, agentID := range agentIDs {
		evs := agentEvents[agentID]
		track := agentTrack{
			AgentID:   agentID,
			AgentType: e.getAgentType(agentID),
			Color:     e.getAgentColor(agentID),
			YOffset:   topMargin + (i * (barHeight + barGap)),
		}

		// Build segments for this agent
		track.Segments = e.buildSegments(evs, timeStart, timeEnd, duration, float64(leftMargin), float64(barWidth))
		tracks = append(tracks, track)
	}

	return &timelineData{
		Agents:          tracks,
		TimeStart:       timeStart,
		TimeEnd:         timeEnd,
		Duration:        duration,
		Width:           opts.Width,
		Height:          height,
		BarWidth:        barWidth,
		BarHeight:       barHeight,
		LeftMargin:      leftMargin,
		TopMargin:       topMargin,
		BottomMargin:    bottomMargin,
		SessionName:     opts.SessionName,
		ExportTime:      time.Now(),
		TotalEvents:     len(events),
		Theme:           opts.Theme,
		IncludeLegend:   opts.IncludeLegend,
		IncludeMetadata: opts.IncludeMetadata,
	}
}

func (e *TimelineExporter) buildSegments(events []state.AgentEvent, timeStart, timeEnd time.Time, duration time.Duration, xOffset, barWidth float64) []timeSegment {
	if len(events) == 0 {
		return nil
	}

	var segments []timeSegment

	// Handle time before first event as idle
	if events[0].Timestamp.After(timeStart) {
		seg := e.createSegment(state.TimelineIdle, timeStart, events[0].Timestamp, timeStart, duration, xOffset, barWidth)
		segments = append(segments, seg)
	}

	// Create segments between events
	for i, ev := range events {
		var segEnd time.Time
		if i+1 < len(events) {
			segEnd = events[i+1].Timestamp
		} else {
			segEnd = timeEnd
		}

		if segEnd.After(ev.Timestamp) {
			seg := e.createSegment(ev.State, ev.Timestamp, segEnd, timeStart, duration, xOffset, barWidth)
			segments = append(segments, seg)
		}
	}

	return segments
}

func (e *TimelineExporter) createSegment(st state.TimelineState, start, end time.Time, timeStart time.Time, duration time.Duration, xOffset, barWidth float64) timeSegment {
	// Calculate X positions
	startOffset := start.Sub(timeStart)
	endOffset := end.Sub(timeStart)

	xStart := xOffset + (float64(startOffset) / float64(duration) * barWidth)
	xEnd := xOffset + (float64(endOffset) / float64(duration) * barWidth)

	return timeSegment{
		State:     st,
		StartTime: start,
		EndTime:   end,
		Duration:  end.Sub(start),
		XStart:    xStart,
		XEnd:      xEnd,
		Width:     xEnd - xStart,
		Color:     e.getStateColor(st),
	}
}

func (e *TimelineExporter) getAgentType(agentID string) string {
	if strings.HasPrefix(agentID, "cc") {
		return "claude"
	}
	if strings.HasPrefix(agentID, "cod") {
		return "codex"
	}
	if strings.HasPrefix(agentID, "gmi") {
		return "gemini"
	}
	return "unknown"
}

func (e *TimelineExporter) getAgentColor(agentID string) string {
	t := e.options.Theme
	switch e.getAgentType(agentID) {
	case "claude":
		return t.ClaudeColor
	case "codex":
		return t.CodexColor
	case "gemini":
		return t.GeminiColor
	default:
		return t.TextColor
	}
}

func (e *TimelineExporter) getStateColor(st state.TimelineState) string {
	t := e.options.Theme
	switch st {
	case state.TimelineIdle:
		return t.IdleColor
	case state.TimelineWorking:
		return t.WorkingColor
	case state.TimelineWaiting:
		return t.WaitingColor
	case state.TimelineError:
		return t.ErrorColor
	case state.TimelineStopped:
		return t.StoppedColor
	default:
		return t.IdleColor
	}
}

// SVG template
const svgTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {{.Width}} {{.Height}}" width="{{.Width}}" height="{{.Height}}">
  <defs>
    <style>
      .title { font: bold 18px sans-serif; fill: {{.Theme.TextColor}}; }
      .subtitle { font: 12px sans-serif; fill: {{.Theme.AxisColor}}; }
      .agent-label { font: bold 12px monospace; }
      .axis-label { font: 10px sans-serif; fill: {{.Theme.AxisColor}}; }
      .legend-text { font: 11px sans-serif; fill: {{.Theme.TextColor}}; }
    </style>
  </defs>

  <!-- Background -->
  <rect width="100%" height="100%" fill="{{.Theme.BackgroundColor}}"/>

  {{if .IncludeMetadata}}
  <!-- Title -->
  <text x="{{.LeftMargin}}" y="25" class="title">Agent Timeline{{if .SessionName}}: {{.SessionName}}{{end}}</text>
  <text x="{{.LeftMargin}}" y="45" class="subtitle">{{.TimeStart.Format "2006-01-02 15:04:05"}} - {{.TimeEnd.Format "15:04:05"}} ({{formatDuration .Duration}}) · {{.TotalEvents}} events</text>
  {{end}}

  <!-- Timeline tracks -->
  {{range .Agents}}
  <!-- Agent: {{.AgentID}} -->
  <text x="10" y="{{add .YOffset 20}}" class="agent-label" fill="{{.Color}}">{{.AgentID}}</text>

  <!-- Background bar -->
  <rect x="{{$.LeftMargin}}" y="{{.YOffset}}" width="{{$.BarWidth}}" height="{{$.BarHeight}}" fill="{{$.Theme.StoppedColor}}" rx="3"/>

  <!-- State segments -->
  {{$trackY := .YOffset}}
  {{range .Segments}}
  <rect x="{{printf "%.1f" .XStart}}" y="{{$trackY}}" width="{{printf "%.1f" .Width}}" height="{{$.BarHeight}}" fill="{{.Color}}" rx="2">
    <title>{{.State}}: {{formatDuration .Duration}}</title>
  </rect>
  {{end}}
  {{end}}

  <!-- Time axis -->
  {{$axisY := add (lastAgentY .Agents .BarHeight 10) 25}}
  <line x1="{{.LeftMargin}}" y1="{{$axisY}}" x2="{{add .LeftMargin .BarWidth}}" y2="{{$axisY}}" stroke="{{.Theme.AxisColor}}" stroke-width="1"/>

  <!-- Time labels -->
  {{range $i, $tick := timeTicks $.TimeStart $.TimeEnd 6}}
  {{$x := tickX $tick $.TimeStart $.Duration $.LeftMargin $.BarWidth}}
  <line x1="{{$x}}" y1="{{$axisY}}" x2="{{$x}}" y2="{{add $axisY 5}}" stroke="{{$.Theme.AxisColor}}" stroke-width="1"/>
  <text x="{{$x}}" y="{{add $axisY 18}}" text-anchor="middle" class="axis-label">{{$tick.Format "15:04"}}</text>
  {{end}}

  {{if .IncludeLegend}}
  <!-- Legend -->
  {{$legendY := add $axisY 40}}
  <text x="{{.LeftMargin}}" y="{{$legendY}}" class="legend-text" font-weight="bold">Legend:</text>

  <rect x="{{add .LeftMargin 60}}" y="{{subtract $legendY 10}}" width="20" height="14" fill="{{.Theme.WorkingColor}}" rx="2"/>
  <text x="{{add .LeftMargin 85}}" y="{{$legendY}}" class="legend-text">Working</text>

  <rect x="{{add .LeftMargin 160}}" y="{{subtract $legendY 10}}" width="20" height="14" fill="{{.Theme.WaitingColor}}" rx="2"/>
  <text x="{{add .LeftMargin 185}}" y="{{$legendY}}" class="legend-text">Waiting</text>

  <rect x="{{add .LeftMargin 260}}" y="{{subtract $legendY 10}}" width="20" height="14" fill="{{.Theme.IdleColor}}" rx="2"/>
  <text x="{{add .LeftMargin 285}}" y="{{$legendY}}" class="legend-text">Idle</text>

  <rect x="{{add .LeftMargin 340}}" y="{{subtract $legendY 10}}" width="20" height="14" fill="{{.Theme.ErrorColor}}" rx="2"/>
  <text x="{{add .LeftMargin 365}}" y="{{$legendY}}" class="legend-text">Error</text>

  <rect x="{{add .LeftMargin 420}}" y="{{subtract $legendY 10}}" width="20" height="14" fill="{{.Theme.StoppedColor}}" rx="2"/>
  <text x="{{add .LeftMargin 445}}" y="{{$legendY}}" class="legend-text">Stopped</text>
  {{end}}

  <!-- Export timestamp -->
  <text x="{{subtract .Width 10}}" y="{{subtract .Height 10}}" text-anchor="end" class="axis-label">Exported: {{.ExportTime.Format "2006-01-02 15:04:05"}}</text>
</svg>
`

func (e *TimelineExporter) renderSVG(data *timelineData) ([]byte, error) {
	funcMap := template.FuncMap{
		"add":      func(a, b int) int { return a + b },
		"subtract": func(a, b int) int { return a - b },
		"formatDuration": func(d time.Duration) string {
			if d < time.Minute {
				return fmt.Sprintf("%ds", int(d.Seconds()))
			}
			if d < time.Hour {
				return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
			}
			return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
		},
		"yOffset": func(topMargin, firstYOffset, barHeight int) int {
			return firstYOffset
		},
		"lastAgentY": func(agents []agentTrack, barHeight, gap int) int {
			if len(agents) == 0 {
				return data.TopMargin
			}
			return agents[len(agents)-1].YOffset + barHeight
		},
		"timeTicks": func(start, end time.Time, count int) []time.Time {
			duration := end.Sub(start)
			interval := duration / time.Duration(count)
			ticks := make([]time.Time, count+1)
			for i := 0; i <= count; i++ {
				ticks[i] = start.Add(time.Duration(i) * interval)
			}
			return ticks
		},
		"tickX": func(t, start time.Time, duration time.Duration, leftMargin, barWidth int) float64 {
			offset := t.Sub(start)
			return float64(leftMargin) + (float64(offset) / float64(duration) * float64(barWidth))
		},
	}

	tmpl, err := template.New("svg").Funcs(funcMap).Parse(svgTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SVG template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute SVG template: %w", err)
	}

	return buf.Bytes(), nil
}

// PNG export using standard library image package
func (e *TimelineExporter) renderPNG(data *timelineData) ([]byte, error) {
	scale := e.options.Scale
	if scale < 1 {
		scale = 1
	}

	width := data.Width * scale
	height := data.Height * scale

	// Create image
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Fill background
	bgColor := parseHexColor(data.Theme.BackgroundColor)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, bgColor)
		}
	}

	// Draw timeline bars
	for _, track := range data.Agents {
		// Draw background bar
		stoppedColor := parseHexColor(data.Theme.StoppedColor)
		drawRect(img,
			data.LeftMargin*scale,
			track.YOffset*scale,
			data.BarWidth*scale,
			data.BarHeight*scale,
			stoppedColor)

		// Draw segments
		for _, seg := range track.Segments {
			segColor := parseHexColor(seg.Color)
			drawRect(img,
				int(seg.XStart)*scale,
				track.YOffset*scale,
				int(seg.Width)*scale,
				data.BarHeight*scale,
				segColor)
		}
	}

	// Encode to PNG
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("failed to encode PNG: %w", err)
	}

	return buf.Bytes(), nil
}

// parseHexColor converts a hex color string to color.RGBA
func parseHexColor(hex string) color.RGBA {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return color.RGBA{128, 128, 128, 255}
	}

	var r, g, b uint8
	_, _ = fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	return color.RGBA{r, g, b, 255}
}

// drawRect draws a filled rectangle on the image
func drawRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	bounds := img.Bounds()
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			px, py := x+dx, y+dy
			if px >= bounds.Min.X && px < bounds.Max.X && py >= bounds.Min.Y && py < bounds.Max.Y {
				img.Set(px, py, c)
			}
		}
	}
}
