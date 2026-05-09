package profiler

import (
	"sort"
	"strings"
	"time"
)

// BottleneckHotspot is a per-(name, phase) aggregation of span data.
// Several spans with the same name+phase are folded into one hotspot row.
type BottleneckHotspot struct {
	Name       string  `json:"name"`
	Phase      string  `json:"phase,omitempty"`
	Count      int     `json:"count"`
	TotalNs    int64   `json:"total_ns"`
	TotalMs    float64 `json:"total_ms"`
	MaxNs      int64   `json:"max_ns"`
	MaxMs      float64 `json:"max_ms"`
	AvgMs      float64 `json:"avg_ms"`
	Percentage float64 `json:"percentage"`

	// Correlation captures the most-recently-seen identifying tags for
	// any span folded into this hotspot. Empty values are dropped so
	// the JSON stays compact.
	Correlation BottleneckCorrelation `json:"correlation,omitzero"`

	// Delta is populated by ComputeTrend; otherwise zero.
	DeltaMs *float64 `json:"delta_ms,omitempty"`
	Trend   string   `json:"trend,omitempty"` // "up", "down", "new", "stable"
}

// BottleneckCorrelation gathers the standard NTM identity fields used
// across robot/serve/tmux/pipeline so a hotspot row is actionable
// without cross-referencing logs.
type BottleneckCorrelation struct {
	Session string `json:"session,omitempty"`
	Pane    string `json:"pane,omitempty"`
	Command string `json:"command,omitempty"`
	RunID   string `json:"run_id,omitempty"`
}

// IsZero returns true when no correlation field is set; used by JSON
// omitempty to keep output compact.
func (c BottleneckCorrelation) IsZero() bool {
	return c.Session == "" && c.Pane == "" && c.Command == "" && c.RunID == ""
}

// PhaseSummary is the per-phase rollup that accompanies the top-N
// hotspot table in BottleneckSnapshot.
type PhaseSummary struct {
	Phase      string  `json:"phase"`
	SpanCount  int     `json:"span_count"`
	TotalMs    float64 `json:"total_ms"`
	Percentage float64 `json:"percentage"`
}

// BottleneckSnapshot is the stable robot-readable surface for the
// bottleneck dashboard. It is safe to serialize directly to JSON.
type BottleneckSnapshot struct {
	Success      bool                `json:"success"`
	Timestamp    string              `json:"timestamp"`
	WindowMs     float64             `json:"window_ms"`
	SpanCount    int                 `json:"span_count"`
	UnendedSpans int                 `json:"unended_spans,omitempty"`
	TotalMs      float64             `json:"total_ms"`
	Hotspots     []BottleneckHotspot `json:"hotspots"`
	Phases       []PhaseSummary      `json:"phases,omitempty"`
}

// BottleneckOptions configures ComputeHotspots.
type BottleneckOptions struct {
	// TopN caps the returned hotspot list. <=0 means no cap.
	TopN int
	// PhaseFilter restricts aggregation to spans whose Phase is in the
	// allow-list. nil/empty means all phases are included.
	PhaseFilter []string
	// MinDuration drops folded hotspots whose TotalNs is below this.
	// Zero means keep everything.
	MinDuration time.Duration
	// Now is overridable for deterministic tests.
	Now func() time.Time
}

// ComputeHotspots folds the supplied spans into a BottleneckSnapshot.
// Spans are grouped by (Name, Phase); the count, total/max/avg time
// and the most-recently-seen correlation tags are recorded for each
// group. Output is sorted by TotalNs descending with alphabetical
// ties so two equal-length runs produce identical JSON.
func ComputeHotspots(spans []*Span, opts BottleneckOptions) BottleneckSnapshot {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	allowed := buildPhaseSet(opts.PhaseFilter)

	type key struct{ name, phase string }
	groups := make(map[key]*BottleneckHotspot)

	var totalNs int64
	spanCount := 0
	unended := 0
	phaseAgg := make(map[string]*PhaseSummary)

	for _, s := range spans {
		if s == nil {
			continue
		}
		if !s.ended || s.Duration <= 0 {
			unended++
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[s.Phase]; !ok {
				continue
			}
		}
		spanCount++
		totalNs += s.Duration.Nanoseconds()

		k := key{name: s.Name, phase: s.Phase}
		h, ok := groups[k]
		if !ok {
			h = &BottleneckHotspot{Name: s.Name, Phase: s.Phase}
			groups[k] = h
		}
		h.Count++
		h.TotalNs += s.Duration.Nanoseconds()
		if s.Duration.Nanoseconds() > h.MaxNs {
			h.MaxNs = s.Duration.Nanoseconds()
		}
		mergeCorrelationFromTags(&h.Correlation, s.Tags)

		if s.Phase != "" {
			ps, ok := phaseAgg[s.Phase]
			if !ok {
				ps = &PhaseSummary{Phase: s.Phase}
				phaseAgg[s.Phase] = ps
			}
			ps.SpanCount++
			ps.TotalMs += float64(s.Duration.Nanoseconds()) / 1e6
		}
	}

	// Materialize hotspots: drop sub-MinDuration rows, compute derived
	// metrics, sort deterministically.
	hotspots := make([]BottleneckHotspot, 0, len(groups))
	for _, h := range groups {
		if opts.MinDuration > 0 && time.Duration(h.TotalNs) < opts.MinDuration {
			continue
		}
		h.TotalMs = float64(h.TotalNs) / 1e6
		h.MaxMs = float64(h.MaxNs) / 1e6
		if h.Count > 0 {
			h.AvgMs = h.TotalMs / float64(h.Count)
		}
		if totalNs > 0 {
			h.Percentage = float64(h.TotalNs) / float64(totalNs) * 100
		}
		hotspots = append(hotspots, *h)
	}
	sort.SliceStable(hotspots, func(i, j int) bool {
		if hotspots[i].TotalNs != hotspots[j].TotalNs {
			return hotspots[i].TotalNs > hotspots[j].TotalNs
		}
		if hotspots[i].Name != hotspots[j].Name {
			return hotspots[i].Name < hotspots[j].Name
		}
		return hotspots[i].Phase < hotspots[j].Phase
	})
	if opts.TopN > 0 && len(hotspots) > opts.TopN {
		hotspots = hotspots[:opts.TopN]
	}

	// Build phase summary, percentages over selected window.
	phases := make([]PhaseSummary, 0, len(phaseAgg))
	for _, ps := range phaseAgg {
		if totalNs > 0 {
			ps.Percentage = ps.TotalMs * 1e6 / float64(totalNs) * 100
		}
		phases = append(phases, *ps)
	}
	sort.Slice(phases, func(i, j int) bool {
		if phases[i].TotalMs != phases[j].TotalMs {
			return phases[i].TotalMs > phases[j].TotalMs
		}
		return phases[i].Phase < phases[j].Phase
	})

	totalMs := float64(totalNs) / 1e6
	return BottleneckSnapshot{
		Success:      true,
		Timestamp:    now().UTC().Format(time.RFC3339Nano),
		WindowMs:     totalMs,
		SpanCount:    spanCount,
		UnendedSpans: unended,
		TotalMs:      totalMs,
		Hotspots:     hotspots,
		Phases:       phases,
	}
}

// LiveBottleneck folds the global profiler's spans into a snapshot.
// Returns an empty snapshot (Success=true, no Hotspots) when profiling
// is disabled so callers always get a valid robot JSON envelope.
func LiveBottleneck(opts BottleneckOptions) BottleneckSnapshot {
	if !IsEnabled() {
		now := opts.Now
		if now == nil {
			now = time.Now
		}
		return BottleneckSnapshot{
			Success:   true,
			Timestamp: now().UTC().Format(time.RFC3339Nano),
		}
	}
	return ComputeHotspots(GetSpans(), opts)
}

// ComputeTrend annotates `current` hotspots with delta + trend strings
// computed against `prior`. A hotspot present in current but not prior
// is marked "new"; one whose total dropped is "down"; one whose total
// rose by at least changeMs is "up"; otherwise "stable".
//
// changeMs <= 0 disables the threshold (any non-zero delta classifies
// as up/down). bd-2eif6: a negative changeMs is clamped to 0 to honor
// the "<=0 disables" contract — without the clamp, raw `d > changeMs`
// and `-d > changeMs` against a negative threshold misclassify small
// drops and zero-deltas as "up".
func ComputeTrend(current, prior BottleneckSnapshot, changeMs float64) BottleneckSnapshot {
	threshold := changeMs
	if threshold < 0 {
		threshold = 0
	}
	priorMap := make(map[string]float64, len(prior.Hotspots))
	for _, h := range prior.Hotspots {
		priorMap[hotspotKey(h)] = h.TotalMs
	}
	annotated := make([]BottleneckHotspot, len(current.Hotspots))
	for i, h := range current.Hotspots {
		annotated[i] = h
		k := hotspotKey(h)
		prevMs, existed := priorMap[k]
		switch {
		case !existed:
			d := h.TotalMs
			annotated[i].DeltaMs = &d
			annotated[i].Trend = "new"
		default:
			d := h.TotalMs - prevMs
			annotated[i].DeltaMs = &d
			switch {
			case d > threshold:
				annotated[i].Trend = "up"
			case -d > threshold:
				annotated[i].Trend = "down"
			default:
				annotated[i].Trend = "stable"
			}
		}
	}
	out := current
	out.Hotspots = annotated
	return out
}

// hotspotKey concatenates the identifying fields of a hotspot row.
func hotspotKey(h BottleneckHotspot) string {
	return h.Name + "|" + h.Phase
}

// buildPhaseSet returns nil when no filter is supplied so callers can
// short-circuit the membership check.
func buildPhaseSet(in []string) map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		out[s] = struct{}{}
	}
	return out
}

// mergeCorrelationFromTags merges identifying tags into c, preferring
// the latest non-empty value seen for each field. Tags use canonical
// keys ("session", "pane", "command", "run_id") so callers across the
// codebase produce comparable hotspot rows.
func mergeCorrelationFromTags(c *BottleneckCorrelation, tags Tags) {
	if len(tags) == 0 {
		return
	}
	if v := tagString(tags, "session"); v != "" {
		c.Session = v
	}
	if v := tagString(tags, "pane"); v != "" {
		c.Pane = v
	}
	if v := tagString(tags, "command"); v != "" {
		c.Command = v
	}
	if v := tagString(tags, "run_id"); v != "" {
		c.RunID = v
	}
}

// tagString returns the tag's string value, or "" if missing or
// non-string. Numeric tags (e.g. pane index 1) are stringified.
func tagString(tags Tags, key string) string {
	v, ok := tags[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case int:
		return itoa(int64(t))
	case int64:
		return itoa(t)
	default:
		return ""
	}
}

// itoa is a tiny strconv replacement to avoid pulling strconv only for
// this one use; supports the small int range we expect in tags.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
