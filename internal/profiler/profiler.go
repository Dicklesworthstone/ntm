// Package profiler provides timing instrumentation for NTM operations.
// Use this package to track performance of startup, commands, and subsystems.
package profiler

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Profiler tracks timing information for operations
type Profiler struct {
	mu      sync.RWMutex
	spans   []*Span
	enabled bool
	start   time.Time
}

// Span represents a timed operation
type Span struct {
	Name      string        `json:"name"`
	Phase     string        `json:"phase,omitempty"` // e.g., "startup", "command", "shutdown"
	StartTime time.Time     `json:"start_time"`
	EndTime   time.Time     `json:"end_time,omitempty"`
	Duration  time.Duration `json:"duration_ns,omitempty"`
	Parent    string        `json:"parent,omitempty"`
	Tags      Tags          `json:"tags,omitempty"`
	children  []*Span
	ended     bool
	tracked   bool // true if this span is being tracked by the global profiler
}

// Tags are key-value annotations on spans
type Tags map[string]interface{}

// Global profiler instance (disabled by default)
var global = &Profiler{enabled: false}

// Enable turns on profiling globally
func Enable() {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.enabled = true
	global.start = time.Now()
}

// IsEnabled returns whether profiling is active
func IsEnabled() bool {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.enabled
}

// Start begins a new span with the given name
func Start(name string) *Span {
	return StartWithPhase(name, "")
}

// MaxSpans is the maximum number of spans to retain in memory
const MaxSpans = 10000

// StartWithPhase begins a new span with name and phase
func StartWithPhase(name, phase string) *Span {
	global.mu.Lock()
	defer global.mu.Unlock()

	if !global.enabled {
		return &Span{Name: name, Phase: phase} // No-op span
	}

	if len(global.spans) >= MaxSpans {
		return &Span{Name: name, Phase: phase} // Untracked due to limit
	}

	span := &Span{
		Name:      name,
		Phase:     phase,
		StartTime: time.Now(),
		Tags:      make(Tags),
		tracked:   true,
	}
	global.spans = append(global.spans, span)
	return span
}

// End finishes the span and records duration
func (s *Span) End() {
	if !s.tracked {
		return
	}

	global.mu.Lock()
	defer global.mu.Unlock()

	if s.ended {
		return
	}
	s.EndTime = time.Now()
	s.Duration = s.EndTime.Sub(s.StartTime)
	s.ended = true
}

// Tag adds a tag to the span
func (s *Span) Tag(key string, value interface{}) *Span {
	if !s.tracked {
		return s
	}

	global.mu.Lock()
	defer global.mu.Unlock()
	if s.Tags == nil {
		s.Tags = make(Tags)
	}
	s.Tags[key] = value
	return s
}

// GetSpansByPhase returns spans filtered by phase
func GetSpansByPhase(phase string) []*Span {
	global.mu.RLock()
	defer global.mu.RUnlock()

	var result []*Span
	for _, s := range global.spans {
		if s.Phase == phase {
			result = append(result, cloneSpanSnapshot(s))
		}
	}
	return result
}

func cloneSpanSnapshot(s *Span) *Span {
	if s == nil {
		return nil
	}

	snapshot := *s
	snapshot.Tags = cloneTags(s.Tags)
	snapshot.tracked = false
	if len(s.children) > 0 {
		snapshot.children = make([]*Span, len(s.children))
		for i, child := range s.children {
			snapshot.children[i] = cloneSpanSnapshot(child)
		}
	}
	return &snapshot
}

func cloneTags(tags Tags) Tags {
	if tags == nil {
		return nil
	}
	out := make(Tags, len(tags))
	for k, v := range tags {
		out[k] = v
	}
	return out
}

// Profile is a timing summary for JSON output
type Profile struct {
	TotalDuration   time.Duration    `json:"total_duration_ns"`
	TotalDurationMs float64          `json:"total_duration_ms"`
	SpanCount       int              `json:"span_count"`
	Spans           []*SpanReport    `json:"spans"`
	Phases          []PhaseReport    `json:"phases,omitempty"`
	Recommendations []Recommendation `json:"recommendations,omitempty"`
}

// SpanReport is a span formatted for output
type SpanReport struct {
	Name       string  `json:"name"`
	Phase      string  `json:"phase,omitempty"`
	DurationNs int64   `json:"duration_ns"`
	DurationMs float64 `json:"duration_ms"`
	Parent     string  `json:"parent,omitempty"`
	Tags       Tags    `json:"tags,omitempty"`
}

// PhaseReport aggregates timing by phase
type PhaseReport struct {
	Phase      string  `json:"phase"`
	SpanCount  int     `json:"span_count"`
	TotalNs    int64   `json:"total_ns"`
	TotalMs    float64 `json:"total_ms"`
	Percentage float64 `json:"percentage"`
}

// GetProfile returns a summary of all profiling data
func GetProfile() Profile {
	global.mu.RLock()
	defer global.mu.RUnlock()

	total := time.Since(global.start)
	profile := Profile{
		TotalDuration:   total,
		TotalDurationMs: float64(total.Nanoseconds()) / 1e6,
		SpanCount:       len(global.spans),
		Spans:           make([]*SpanReport, 0, len(global.spans)),
	}

	// Build span reports and aggregate by phase
	phaseStats := make(map[string]*PhaseReport)

	for _, s := range global.spans {
		report := &SpanReport{
			Name:       s.Name,
			Phase:      s.Phase,
			DurationNs: s.Duration.Nanoseconds(),
			DurationMs: float64(s.Duration.Nanoseconds()) / 1e6,
			Parent:     s.Parent,
		}
		if len(s.Tags) > 0 {
			report.Tags = cloneTags(s.Tags)
		}
		profile.Spans = append(profile.Spans, report)

		// Aggregate by phase
		if s.Phase != "" {
			if _, ok := phaseStats[s.Phase]; !ok {
				phaseStats[s.Phase] = &PhaseReport{Phase: s.Phase}
			}
			phaseStats[s.Phase].SpanCount++
			phaseStats[s.Phase].TotalNs += s.Duration.Nanoseconds()
		}
	}

	// Build phase reports with percentages
	for _, ps := range phaseStats {
		ps.TotalMs = float64(ps.TotalNs) / 1e6
		if total.Nanoseconds() > 0 {
			ps.Percentage = float64(ps.TotalNs) / float64(total.Nanoseconds()) * 100
		}
		profile.Phases = append(profile.Phases, *ps)
	}

	return profile
}

// WriteJSON writes the profile as JSON
func WriteJSON(w io.Writer) error {
	profile := GetProfile()
	profile.Recommendations = GetRecommendations()
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(profile)
}

// WriteText writes a human-readable profile summary
func WriteText(w io.Writer) error {
	profile := GetProfile()

	fmt.Fprintf(w, "=== NTM Performance Profile ===\n")
	fmt.Fprintf(w, "Total Duration: %.2fms\n", profile.TotalDurationMs)
	fmt.Fprintf(w, "Span Count: %d\n\n", profile.SpanCount)

	if len(profile.Phases) > 0 {
		fmt.Fprintf(w, "By Phase:\n")
		for _, p := range profile.Phases {
			fmt.Fprintf(w, "  %-15s %6.2fms (%5.1f%%) [%d spans]\n",
				p.Phase+":", p.TotalMs, p.Percentage, p.SpanCount)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "Top Spans by Duration:\n")
	// Sort spans by duration (descending)
	spans := make([]*SpanReport, len(profile.Spans))
	copy(spans, profile.Spans)
	sort.Slice(spans, func(i, j int) bool {
		return spans[i].DurationNs > spans[j].DurationNs
	})
	limit := 10
	if len(spans) < limit {
		limit = len(spans)
	}
	for i := 0; i < limit; i++ {
		s := spans[i]
		fmt.Fprintf(w, "  %6.2fms  %s", s.DurationMs, s.Name)
		if s.Phase != "" {
			fmt.Fprintf(w, " [%s]", s.Phase)
		}
		fmt.Fprintln(w)
	}

	// Add recommendations section
	recs := GetRecommendations()
	if len(recs) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Recommendations:\n")
		for _, r := range recs {
			severityIcon := "ℹ"
			switch r.Severity {
			case SeverityCritical:
				severityIcon = "❌"
			case SeverityWarning:
				severityIcon = "⚠"
			}
			fmt.Fprintf(w, "  %s [%s] %s\n", severityIcon, r.Category, r.Message)
			fmt.Fprintf(w, "     → %s\n", r.Suggestion)
		}
	}

	return nil
}
