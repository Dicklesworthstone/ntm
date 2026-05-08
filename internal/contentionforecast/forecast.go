// Package contentionforecast predicts conflict-prone files before
// assignment by mining bead labels, recent file reservations, closed-
// bead history, and git-touched paths.
//
// This is a forecast, not a real-time conflict detector — internal/
// coordinator already detects active conflicts. The forecaster's job
// is to flag patterns that are likely to be contended *next* so work
// can be routed around hotspots.
//
// First slice is heuristic and deterministic. Each Hotspot's Factors
// map exposes every contributing signal so an operator can audit the
// score; nothing is opaque. The package never mutates reservations
// or files.
//
// See bd-3v1gs.11.
package contentionforecast

import (
	"math"
	"sort"
	"strings"
	"time"
)

// ReservationEpisode is one historical reservation window. Conflicted
// is true when another agent's reservation overlapped or was rejected
// against this one — set by the caller from coordinator history.
type ReservationEpisode struct {
	PathPattern string
	AgentName   string
	AcquiredAt  time.Time
	ReleasedAt  time.Time
	Conflicted  bool
}

// ClosedBead is one recently-closed bead with its labels and the
// paths it touched. Used to score "labels frequently appear on this
// path" — a label cluster that recurs on a pattern is a contention
// signal.
type ClosedBead struct {
	ID       string
	Labels   []string
	Paths    []string
	ClosedAt time.Time
}

// TouchedPath is one git-touched file with how many distinct commits
// have touched it recently and when. Broad activity on a single file
// is a contention signal even without reservations.
type TouchedPath struct {
	Path        string
	TouchCount  int
	LastTouched time.Time
}

// Inputs is the full evidence the forecaster reduces.
type Inputs struct {
	Reservations  []ReservationEpisode
	ClosedBeads   []ClosedBead
	TouchedPaths  []TouchedPath
	BeadLabels    map[string][]string

	// DecayHalfLife controls how fast historical signals lose weight.
	// Sentinel handling:
	//   - zero          → use DefaultDecayHalfLife (14 days)
	//   - negative      → disable decay entirely (every event counts at
	//                     full weight, regardless of age)
	//   - positive      → use that duration as the half-life
	//
	// Use a small negative sentinel like -1 to ask for "no decay";
	// passing a literal 0 selects the default and is intentionally not
	// the same as "disabled" so callers that leave the field unset get
	// the recommended decay shape rather than accidentally turning the
	// forecast into an unweighted historical sum.
	DecayHalfLife time.Duration

	// Now lets tests pin the wall clock.
	Now time.Time

	// MinSamplesForHigh and MinSamplesForMedium are the sample-count
	// thresholds for confidence classification. Defaults: 20 / 5.
	MinSamplesForHigh   int
	MinSamplesForMedium int
}

// DefaultDecayHalfLife is 14 days — recent reservations are given
// roughly twice the weight of two-week-old ones.
const DefaultDecayHalfLife = 14 * 24 * time.Hour

// Confidence is the operator-facing certainty of a forecast.
type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Hotspot is one forecast row. Score and Confidence are independent:
// a high-score pattern with few samples can still be low-confidence,
// and a low-score pattern with many samples can be high-confidence
// (i.e., we are sure it is *not* contended).
type Hotspot struct {
	PathPattern  string             `json:"path_pattern"`
	Score        float64            `json:"score"`
	Confidence   Confidence         `json:"confidence"`
	SampleCount  int                `json:"sample_count"`
	Factors      map[string]float64 `json:"factors,omitempty"`
	LikelyOwners []string           `json:"likely_owners,omitempty"`
	Alternatives []string           `json:"alternatives,omitempty"`
}

// Forecast is the full report.
type Forecast struct {
	GeneratedAt time.Time `json:"generated_at"`
	Hotspots    []Hotspot `json:"hotspots"`
	Notes       []string  `json:"notes,omitempty"`
}

// Compute reduces inputs into a Forecast. Pure: no I/O.
//
// Algorithm:
//  1. Aggregate per-pattern stats from Reservations: total count,
//     conflict count, distinct owners, last-acquired timestamp,
//     decayed weight sum.
//  2. Bring in ClosedBead overlap: a bead whose Paths intersect a
//     pattern adds a label-cluster bonus weighted by how many other
//     closed beads share its labels.
//  3. Bring in TouchedPath activity: a path with high TouchCount
//     and a Pattern that covers it adds a git-activity bonus.
//  4. Apply broad-glob penalty: patterns ending in "**" get a soft
//     penalty so a too-broad lane doesn't dominate the report.
//  5. Combine factors into Score in [0,1] via squashing.
//  6. Choose Confidence from sample count.
//  7. Suggest narrower Alternatives when the pattern is broad.
func Compute(in Inputs) Forecast {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	halfLife := in.DecayHalfLife
	if halfLife == 0 {
		halfLife = DefaultDecayHalfLife
	}
	highT := in.MinSamplesForHigh
	if highT == 0 {
		highT = 20
	}
	medT := in.MinSamplesForMedium
	if medT == 0 {
		medT = 5
	}

	out := Forecast{
		GeneratedAt: now.UTC(),
		Hotspots:    []Hotspot{},
		Notes:       []string{"heuristic forecast; factors are exposed for audit"},
	}

	// Per-pattern aggregates.
	type aggregate struct {
		count          int
		conflicts      int
		decayedWeight  float64
		owners         map[string]int
		lastAcquiredAt time.Time
		// label/touch bonuses computed below.
		labelOverlap   float64
		touchSignal    float64
	}
	agg := make(map[string]*aggregate)
	getAgg := func(p string) *aggregate {
		a, ok := agg[p]
		if !ok {
			a = &aggregate{owners: make(map[string]int)}
			agg[p] = a
		}
		return a
	}

	for _, r := range in.Reservations {
		p := strings.TrimSpace(r.PathPattern)
		if p == "" {
			continue
		}
		w := decayWeight(now.Sub(r.AcquiredAt), halfLife)
		a := getAgg(p)
		a.count++
		if r.Conflicted {
			a.conflicts++
		}
		a.decayedWeight += w
		if name := strings.TrimSpace(r.AgentName); name != "" {
			a.owners[name]++
		}
		if r.AcquiredAt.After(a.lastAcquiredAt) {
			a.lastAcquiredAt = r.AcquiredAt
		}
	}

	// Label-cluster bonus: for each pair of closed beads that share
	// a non-empty label and touched any same path, attribute a small
	// bonus to every pattern that matches the shared path.
	labelBonus := computeLabelClusterBonus(in.ClosedBeads, halfLife, now)
	for path, bonus := range labelBonus {
		// The label bonus is keyed by exact path; map it onto every
		// pattern whose match includes this path.
		for p := range agg {
			if patternCoversPath(p, path) {
				getAgg(p).labelOverlap += bonus
			}
		}
		// Also surface the path itself as a candidate hotspot, so a
		// busy single file lacking a reservation history still
		// appears in the forecast.
		if _, exists := agg[path]; !exists {
			a := getAgg(path)
			a.labelOverlap += bonus
		}
	}

	// Git-touch bonus.
	touchBonus := computeTouchBonus(in.TouchedPaths, halfLife, now)
	for path, bonus := range touchBonus {
		for p := range agg {
			if patternCoversPath(p, path) {
				getAgg(p).touchSignal += bonus
			}
		}
		if _, exists := agg[path]; !exists {
			getAgg(path).touchSignal += bonus
		}
	}

	// Build hotspots.
	for pattern, a := range agg {
		conflictRate := 0.0
		if a.count > 0 {
			conflictRate = float64(a.conflicts) / float64(a.count)
		}
		broadPenalty := broadGlobPenalty(pattern)
		// frequency factor saturates so a single overheated pattern
		// doesn't dwarf everything else.
		freq := squash01(a.decayedWeight)
		factors := map[string]float64{
			"frequency":    round3(freq),
			"conflict_rate": round3(conflictRate),
			"label_overlap": round3(squash01(a.labelOverlap)),
			"git_activity":  round3(squash01(a.touchSignal)),
			"broad_glob_penalty": round3(broadPenalty),
		}
		// Weighted combination. Weights chosen so reservation
		// frequency + conflict rate dominate, with label/git as
		// secondary signals; broad-glob is a multiplicative dampener.
		raw := 0.40*factors["frequency"] +
			0.35*factors["conflict_rate"] +
			0.15*factors["label_overlap"] +
			0.10*factors["git_activity"]
		score := raw * (1 - broadPenalty)
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}

		conf := classifyConfidence(a.count, highT, medT)
		owners := topOwners(a.owners, 3)

		hs := Hotspot{
			PathPattern:  pattern,
			Score:        round3(score),
			Confidence:   conf,
			SampleCount:  a.count,
			Factors:      factors,
			LikelyOwners: owners,
		}
		if broadPenalty > 0 {
			hs.Alternatives = suggestAlternatives(pattern, in.TouchedPaths, in.ClosedBeads)
		}
		out.Hotspots = append(out.Hotspots, hs)
	}

	// Drop zero-score hotspots — keeping them adds noise to the
	// report. A hotspot with zero score has no signal driving it.
	filtered := make([]Hotspot, 0, len(out.Hotspots))
	for _, h := range out.Hotspots {
		if h.Score > 0 {
			filtered = append(filtered, h)
		}
	}
	out.Hotspots = filtered

	// Stable sort: score desc, then pattern asc.
	sort.SliceStable(out.Hotspots, func(i, j int) bool {
		if out.Hotspots[i].Score != out.Hotspots[j].Score {
			return out.Hotspots[i].Score > out.Hotspots[j].Score
		}
		return out.Hotspots[i].PathPattern < out.Hotspots[j].PathPattern
	})

	return out
}

// decayWeight returns 1 for events at age 0, 0.5 at one half-life,
// 0.25 at two half-lives, etc. A negative half-life disables decay
// (always returns 1). A zero half-life is unreachable from Compute
// (which substitutes DefaultDecayHalfLife) but is also treated as
// "disabled" here so direct callers of decayWeight see consistent
// behavior with the public Inputs.DecayHalfLife semantics.
func decayWeight(age, halfLife time.Duration) float64 {
	if halfLife <= 0 {
		return 1
	}
	if age <= 0 {
		return 1
	}
	return math.Pow(0.5, float64(age)/float64(halfLife))
}

// squash01 maps a non-negative raw weight into [0,1) via x/(x+1).
// The shape is gentle enough that 1.0 raw ≈ 0.50 and 5.0 ≈ 0.83,
// so distinct raw weights remain distinguishable in the score.
func squash01(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return x / (x + 1)
}

// broadGlobPenalty returns a value in [0, 0.6] that grows with how
// many path components the pattern wildcards over. "**" alone is the
// worst (covers everything); a top-level "**" with one prefix ("foo/**")
// gets a moderate penalty; deeper-prefix "**" gets less.
func broadGlobPenalty(pattern string) float64 {
	p := strings.TrimSpace(pattern)
	switch {
	case p == "" || p == "**" || p == "/**":
		return 0.6
	case strings.HasSuffix(p, "/**"):
		// Penalty scales inversely with prefix depth.
		prefix := strings.TrimSuffix(p, "/**")
		depth := strings.Count(prefix, "/") + 1
		switch {
		case depth <= 1:
			return 0.45
		case depth == 2:
			return 0.20
		default:
			return 0.05
		}
	default:
		return 0
	}
}

func classifyConfidence(samples, highT, medT int) Confidence {
	switch {
	case samples >= highT:
		return ConfidenceHigh
	case samples >= medT:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

func topOwners(owners map[string]int, n int) []string {
	if len(owners) == 0 {
		return nil
	}
	type kv struct {
		name  string
		count int
	}
	pairs := make([]kv, 0, len(owners))
	for name, count := range owners {
		pairs = append(pairs, kv{name, count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].name < pairs[j].name
	})
	if len(pairs) > n {
		pairs = pairs[:n]
	}
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.name
	}
	return out
}

// computeLabelClusterBonus folds in label co-occurrence: for each
// path P touched by closed bead B, count how many *other* closed
// beads share at least one label with B and touched anything near P.
// The result is keyed by exact path. Decayed by ClosedAt age.
func computeLabelClusterBonus(beads []ClosedBead, halfLife time.Duration, now time.Time) map[string]float64 {
	result := make(map[string]float64)
	if len(beads) < 2 {
		return result
	}
	// Build label -> bead index for quick lookup.
	labelToBeads := make(map[string][]int, 16)
	for i, b := range beads {
		for _, l := range b.Labels {
			l = strings.TrimSpace(strings.ToLower(l))
			if l == "" {
				continue
			}
			labelToBeads[l] = append(labelToBeads[l], i)
		}
	}
	for i, b := range beads {
		coBeads := make(map[int]struct{})
		for _, l := range b.Labels {
			l = strings.TrimSpace(strings.ToLower(l))
			for _, j := range labelToBeads[l] {
				if j == i {
					continue
				}
				coBeads[j] = struct{}{}
			}
		}
		w := decayWeight(now.Sub(b.ClosedAt), halfLife) * float64(len(coBeads))
		if w == 0 {
			continue
		}
		for _, p := range b.Paths {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			result[p] += w
		}
	}
	return result
}

func computeTouchBonus(paths []TouchedPath, halfLife time.Duration, now time.Time) map[string]float64 {
	out := make(map[string]float64, len(paths))
	for _, p := range paths {
		path := strings.TrimSpace(p.Path)
		if path == "" || p.TouchCount <= 0 {
			continue
		}
		w := decayWeight(now.Sub(p.LastTouched), halfLife) * float64(p.TouchCount)
		out[path] += w
	}
	return out
}

// patternCoversPath returns true when `path` would be matched by a
// reservation pattern — basic exact / suffix-/** / suffix-/* support.
func patternCoversPath(pattern, path string) bool {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return false
	}
	if p == path {
		return true
	}
	if strings.HasSuffix(p, "/**") {
		prefix := strings.TrimSuffix(p, "/**")
		if prefix == "" {
			return true
		}
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	if strings.HasSuffix(p, "/*") {
		prefix := strings.TrimSuffix(p, "/*")
		if !strings.HasPrefix(path, prefix+"/") {
			return false
		}
		rest := strings.TrimPrefix(path, prefix+"/")
		return rest != "" && !strings.Contains(rest, "/")
	}
	return false
}

// suggestAlternatives proposes narrower scopes for a broad pattern
// based on which sub-paths actually saw activity.
//
// bd-r8ybt: the catch-all patterns "" / "**" / "/**" all earn the
// maximum broad-glob penalty (0.6) but pre-fix produced no
// alternatives — "**" failed the HasSuffix("/**") check, and "/**"
// produced an empty prefix that rejected every relative path. For
// these patterns, derive alternatives from the top-level directory
// segment of each observed touch / bead path so an operator narrows
// from "**" to a concrete "<top_dir>/**".
func suggestAlternatives(pattern string, touched []TouchedPath, beads []ClosedBead) []string {
	pattern = strings.TrimSpace(pattern)

	// Catch-all branch for the maximally-broad shapes.
	if pattern == "" || pattern == "**" || pattern == "/**" {
		return suggestTopLevelAlternatives(touched, beads)
	}

	if !strings.HasSuffix(pattern, "/**") {
		return nil
	}
	prefix := strings.TrimSuffix(pattern, "/**")
	subdirs := make(map[string]int)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(p, prefix+"/") {
			return
		}
		rest := strings.TrimPrefix(p, prefix+"/")
		// First subdirectory only — narrow alternatives are typically
		// "<prefix>/<sub>/**".
		if idx := strings.IndexByte(rest, '/'); idx > 0 {
			subdirs[rest[:idx]]++
		}
	}
	for _, t := range touched {
		add(t.Path)
	}
	for _, b := range beads {
		for _, p := range b.Paths {
			add(p)
		}
	}
	if len(subdirs) == 0 {
		return nil
	}
	return formatTopAlternatives(subdirs, prefix+"/")
}

// suggestTopLevelAlternatives is the catch-all branch used when the
// pattern is "**" / "/**" / "". It extracts the top-level directory
// from each observed path and emits "<top_dir>/**" suggestions.
func suggestTopLevelAlternatives(touched []TouchedPath, beads []ClosedBead) []string {
	tops := make(map[string]int)
	add := func(p string) {
		p = strings.TrimSpace(p)
		// Strip a leading "/" so absolute and relative paths share the
		// same top-level segment.
		p = strings.TrimPrefix(p, "/")
		if p == "" {
			return
		}
		if idx := strings.IndexByte(p, '/'); idx > 0 {
			tops[p[:idx]]++
		}
	}
	for _, t := range touched {
		add(t.Path)
	}
	for _, b := range beads {
		for _, p := range b.Paths {
			add(p)
		}
	}
	if len(tops) == 0 {
		return nil
	}
	return formatTopAlternatives(tops, "")
}

// formatTopAlternatives sorts a directory-frequency map by count desc /
// name asc, caps at 3, and renders each entry as "<prefix><name>/**".
// Shared between the prefixed and catch-all suggestion paths so output
// shape stays consistent across the two branches.
func formatTopAlternatives(counts map[string]int, prefix string) []string {
	type kv struct {
		name  string
		count int
	}
	pairs := make([]kv, 0, len(counts))
	for k, v := range counts {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].name < pairs[j].name
	})
	if len(pairs) > 3 {
		pairs = pairs[:3]
	}
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = prefix + p.name + "/**"
	}
	return out
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
