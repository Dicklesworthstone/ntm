package profiler

import (
	"fmt"
	"sort"
	"time"
)

// Recommendation is a suggested optimization based on profiling data
type Recommendation struct {
	Severity   Severity `json:"severity"`
	Category   string   `json:"category"`
	Message    string   `json:"message"`
	SpanName   string   `json:"span_name,omitempty"`
	DurationMs float64  `json:"duration_ms,omitempty"`
	Suggestion string   `json:"suggestion"`
}

// Severity indicates the importance of a recommendation
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Thresholds for recommendations
type Thresholds struct {
	// Startup thresholds
	StartupWarningMs  float64 // Warn if startup exceeds this
	StartupCriticalMs float64 // Critical if startup exceeds this

	// Span thresholds
	SlowSpanMs float64 // Individual span considered slow
	VerySlowMs float64 // Individual span considered very slow

	// Phase thresholds
	PhasePercentWarn float64 // Warn if phase takes more than this % of total
}

// DefaultThresholds returns sensible default timing thresholds
func DefaultThresholds() Thresholds {
	return Thresholds{
		StartupWarningMs:  500,  // 500ms startup is slow
		StartupCriticalMs: 1000, // 1s startup is critical
		SlowSpanMs:        100,  // 100ms span is slow
		VerySlowMs:        500,  // 500ms span is very slow
		PhasePercentWarn:  50,   // 50% of time in one phase is concerning
	}
}

// GetRecommendations analyzes profiling data and returns optimization suggestions
func GetRecommendations() []Recommendation {
	return GetRecommendationsWithThresholds(DefaultThresholds())
}

// GetRecommendationsWithThresholds analyzes with custom thresholds
func GetRecommendationsWithThresholds(t Thresholds) []Recommendation {
	profile := GetProfile()
	var recs []Recommendation

	// Check total startup time
	startupSpans := GetSpansByPhase("startup")
	var startupTotal time.Duration
	for _, s := range startupSpans {
		startupTotal += s.Duration
	}
	startupMs := float64(startupTotal.Nanoseconds()) / 1e6

	if startupMs > t.StartupCriticalMs {
		recs = append(recs, Recommendation{
			Severity:   SeverityCritical,
			Category:   "startup",
			Message:    fmt.Sprintf("Startup time is very slow: %.0fms", startupMs),
			DurationMs: startupMs,
			Suggestion: "Consider lazy-loading expensive initialization or using two-phase startup",
		})
	} else if startupMs > t.StartupWarningMs {
		recs = append(recs, Recommendation{
			Severity:   SeverityWarning,
			Category:   "startup",
			Message:    fmt.Sprintf("Startup time is slow: %.0fms", startupMs),
			DurationMs: startupMs,
			Suggestion: "Review startup spans to identify expensive operations that could be deferred",
		})
	}

	// Find slow individual spans
	type spanDuration struct {
		name     string
		phase    string
		duration float64
	}
	var slowSpans []spanDuration

	for _, s := range profile.Spans {
		if s.DurationMs > t.SlowSpanMs {
			slowSpans = append(slowSpans, spanDuration{
				name:     s.Name,
				phase:    s.Phase,
				duration: s.DurationMs,
			})
		}
	}

	// Sort by duration descending
	sort.Slice(slowSpans, func(i, j int) bool {
		return slowSpans[i].duration > slowSpans[j].duration
	})

	// Report top slow spans
	for i, ss := range slowSpans {
		if i >= 5 { // Limit to top 5
			break
		}
		severity := SeverityWarning
		suggestion := "Consider caching, parallelizing, or deferring this operation"
		if ss.duration > t.VerySlowMs {
			severity = SeverityCritical
			suggestion = "This operation is a major bottleneck - consider async execution or removal"
		}
		recs = append(recs, Recommendation{
			Severity:   severity,
			Category:   "slow_span",
			Message:    fmt.Sprintf("Slow operation: %s (%.0fms)", ss.name, ss.duration),
			SpanName:   ss.name,
			DurationMs: ss.duration,
			Suggestion: suggestion,
		})
	}

	// Check phase balance
	for _, p := range profile.Phases {
		if p.Percentage > t.PhasePercentWarn && p.SpanCount > 1 {
			recs = append(recs, Recommendation{
				Severity:   SeverityWarning,
				Category:   "phase_balance",
				Message:    fmt.Sprintf("Phase '%s' consumes %.0f%% of total time", p.Phase, p.Percentage),
				DurationMs: p.TotalMs,
				Suggestion: fmt.Sprintf("Review operations in '%s' phase for optimization opportunities", p.Phase),
			})
		}
	}

	// Check for missing profiling if enabled but no spans
	if IsEnabled() && len(profile.Spans) == 0 {
		recs = append(recs, Recommendation{
			Severity:   SeverityInfo,
			Category:   "instrumentation",
			Message:    "Profiling enabled but no spans recorded",
			Suggestion: "Add profiler.Start/End calls to track operations",
		})
	}

	// Add positive feedback if performance is good
	if len(recs) == 0 && len(profile.Spans) > 0 {
		recs = append(recs, Recommendation{
			Severity:   SeverityInfo,
			Category:   "performance",
			Message:    fmt.Sprintf("Performance looks good (%.0fms total, %d operations)", profile.TotalDurationMs, profile.SpanCount),
			DurationMs: profile.TotalDurationMs,
			Suggestion: "No optimization recommendations at this time",
		})
	}

	return recs
}
