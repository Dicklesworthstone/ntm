package contentionforecast

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func clock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

func TestCompute_NoHistoryProducesEmptyHotspots(t *testing.T) {
	t.Parallel()
	f := Compute(Inputs{Now: clock()})
	if len(f.Hotspots) != 0 {
		t.Errorf("Hotspots = %d, want 0 on empty inputs", len(f.Hotspots))
	}
	if f.GeneratedAt.IsZero() {
		t.Errorf("GeneratedAt unset")
	}
}

// Repeated reservations with conflicts on the same pattern must
// produce a top hotspot with high confidence and a non-zero
// conflict_rate factor.
func TestCompute_RepeatedContentionProducesTopHotspot(t *testing.T) {
	t.Parallel()
	now := clock()
	hot := "internal/auth/**"
	cool := "internal/docs/**"

	episodes := make([]ReservationEpisode, 0, 30)
	// 25 conflicting reservations on the hot pattern.
	for i := 0; i < 25; i++ {
		episodes = append(episodes, ReservationEpisode{
			PathPattern: hot,
			AgentName:   pickAgent(i),
			AcquiredAt:  now.Add(-time.Duration(i+1) * time.Hour),
			ReleasedAt:  now.Add(-time.Duration(i)*time.Hour - 30*time.Minute),
			Conflicted:  i%2 == 0, // half of them clashed
		})
	}
	// 3 calm reservations on the cool pattern.
	for i := 0; i < 3; i++ {
		episodes = append(episodes, ReservationEpisode{
			PathPattern: cool,
			AgentName:   "Docs",
			AcquiredAt:  now.Add(-time.Duration(i+1) * time.Hour),
		})
	}

	f := Compute(Inputs{Reservations: episodes, Now: now})
	if len(f.Hotspots) == 0 {
		t.Fatal("no hotspots produced")
	}
	top := f.Hotspots[0]
	if top.PathPattern != hot {
		t.Errorf("top pattern = %s, want %s", top.PathPattern, hot)
	}
	if top.Confidence != ConfidenceHigh {
		t.Errorf("top confidence = %s, want high (25 samples)", top.Confidence)
	}
	if top.Factors["conflict_rate"] == 0 {
		t.Errorf("conflict_rate = 0, want > 0")
	}
	if top.Factors["frequency"] == 0 {
		t.Errorf("frequency = 0, want > 0")
	}
	if len(top.LikelyOwners) == 0 {
		t.Errorf("LikelyOwners empty; want at least one")
	}
}

// A super-broad glob like "**" must take a penalty so it does not
// dwarf narrower patterns — but it should still surface in the
// report (penalty != elimination).
func TestCompute_BroadGlobGetsPenalty(t *testing.T) {
	t.Parallel()
	now := clock()
	in := Inputs{
		Now: now,
		Reservations: []ReservationEpisode{
			{PathPattern: "**", AgentName: "Wide", AcquiredAt: now.Add(-1 * time.Hour), Conflicted: true},
			{PathPattern: "**", AgentName: "Wide", AcquiredAt: now.Add(-2 * time.Hour), Conflicted: true},
			{PathPattern: "**", AgentName: "Wide", AcquiredAt: now.Add(-3 * time.Hour), Conflicted: true},
			// Narrow: same conflict count, but specific.
			{PathPattern: "internal/auth/session.go", AgentName: "Narrow", AcquiredAt: now.Add(-1 * time.Hour), Conflicted: true},
			{PathPattern: "internal/auth/session.go", AgentName: "Narrow", AcquiredAt: now.Add(-2 * time.Hour), Conflicted: true},
			{PathPattern: "internal/auth/session.go", AgentName: "Narrow", AcquiredAt: now.Add(-3 * time.Hour), Conflicted: true},
		},
	}
	f := Compute(in)
	var wide, narrow *Hotspot
	for i := range f.Hotspots {
		switch f.Hotspots[i].PathPattern {
		case "**":
			wide = &f.Hotspots[i]
		case "internal/auth/session.go":
			narrow = &f.Hotspots[i]
		}
	}
	if wide == nil || narrow == nil {
		t.Fatalf("missing hotspot(s): wide=%v narrow=%v", wide, narrow)
	}
	if wide.Factors["broad_glob_penalty"] <= 0 {
		t.Errorf("broad_glob_penalty for ** = %v, want > 0", wide.Factors["broad_glob_penalty"])
	}
	if narrow.Factors["broad_glob_penalty"] != 0 {
		t.Errorf("broad_glob_penalty for narrow path = %v, want 0", narrow.Factors["broad_glob_penalty"])
	}
	if wide.Score >= narrow.Score {
		t.Errorf("wide.Score=%v >= narrow.Score=%v; broad-glob penalty did not bite", wide.Score, narrow.Score)
	}
}

// Stale reservations must contribute less than fresh ones — same
// count, vastly different ages should produce different scores.
func TestCompute_StaleHistoryDecays(t *testing.T) {
	t.Parallel()
	now := clock()

	freshEpisodes := []ReservationEpisode{
		{PathPattern: "internal/recent/**", AgentName: "A", AcquiredAt: now.Add(-1 * time.Hour), Conflicted: true},
		{PathPattern: "internal/recent/**", AgentName: "A", AcquiredAt: now.Add(-2 * time.Hour), Conflicted: true},
		{PathPattern: "internal/recent/**", AgentName: "A", AcquiredAt: now.Add(-3 * time.Hour), Conflicted: true},
	}
	staleEpisodes := []ReservationEpisode{
		{PathPattern: "internal/old/**", AgentName: "B", AcquiredAt: now.Add(-90 * 24 * time.Hour), Conflicted: true},
		{PathPattern: "internal/old/**", AgentName: "B", AcquiredAt: now.Add(-91 * 24 * time.Hour), Conflicted: true},
		{PathPattern: "internal/old/**", AgentName: "B", AcquiredAt: now.Add(-92 * 24 * time.Hour), Conflicted: true},
	}

	fresh := Compute(Inputs{Reservations: freshEpisodes, Now: now}).Hotspots
	stale := Compute(Inputs{Reservations: staleEpisodes, Now: now}).Hotspots

	if len(fresh) == 0 || len(stale) == 0 {
		t.Fatal("expected hotspots in both fresh and stale runs")
	}
	if fresh[0].Factors["frequency"] <= stale[0].Factors["frequency"] {
		t.Errorf("frequency: fresh=%v stale=%v; decay did not bite",
			fresh[0].Factors["frequency"], stale[0].Factors["frequency"])
	}
	if fresh[0].Score <= stale[0].Score {
		t.Errorf("score: fresh=%v stale=%v; decay did not bite", fresh[0].Score, stale[0].Score)
	}
}

// Decay disabled means same-count stale and fresh produce the same
// score (modulo other factors).
func TestCompute_DecayDisabledKeepsAllSamplesAtFullWeight(t *testing.T) {
	t.Parallel()
	now := clock()
	in := Inputs{
		Now: now,
		Reservations: []ReservationEpisode{
			{PathPattern: "internal/x/**", AgentName: "A", AcquiredAt: now.Add(-100 * 24 * time.Hour), Conflicted: true},
			{PathPattern: "internal/x/**", AgentName: "A", AcquiredAt: now.Add(-2 * time.Hour), Conflicted: true},
		},
	}
	// Zero selects the documented default (14d half-life); negative is
	// the documented "disable decay" sentinel.
	in.DecayHalfLife = 0
	defaultRun := Compute(in).Hotspots[0]
	in.DecayHalfLife = -1
	noDecayRun := Compute(in).Hotspots[0]
	// With decay disabled the run should score >= the default (which
	// uses 14d half-life and penalizes the 100d-old sample).
	if noDecayRun.Score < defaultRun.Score {
		t.Errorf("noDecay.Score=%v default.Score=%v; decay-disabled should score ≥ default",
			noDecayRun.Score, defaultRun.Score)
	}
}

// bd-iwqvb: pin the documented sentinel contract on Inputs.DecayHalfLife.
// Zero must select the 14-day default; negative must disable decay so
// every event lands at full weight regardless of age.
func TestCompute_DecayHalfLifeSentinelContract(t *testing.T) {
	t.Parallel()
	now := clock()
	old := now.Add(-365 * 24 * time.Hour) // a year stale
	rec := now.Add(-1 * time.Hour)        // an hour fresh

	makeInputs := func(half time.Duration) Inputs {
		return Inputs{
			Now:           now,
			DecayHalfLife: half,
			Reservations: []ReservationEpisode{
				{PathPattern: "internal/x/**", AgentName: "A", AcquiredAt: old, Conflicted: true},
				{PathPattern: "internal/x/**", AgentName: "A", AcquiredAt: rec, Conflicted: true},
			},
		}
	}

	zero := Compute(makeInputs(0))
	def := Compute(makeInputs(DefaultDecayHalfLife))
	if len(zero.Hotspots) == 0 || len(def.Hotspots) == 0 {
		t.Fatalf("expected hotspots from both runs; zero=%d default=%d", len(zero.Hotspots), len(def.Hotspots))
	}
	// Zero MUST be byte-equal to passing DefaultDecayHalfLife — that
	// is the documented sentinel.
	if zero.Hotspots[0].Score != def.Hotspots[0].Score {
		t.Errorf("DecayHalfLife=0 score=%v != DefaultDecayHalfLife score=%v; zero must select the default",
			zero.Hotspots[0].Score, def.Hotspots[0].Score)
	}

	// Negative MUST disable decay — every weight is 1, so the year-old
	// reservation contributes the same as the hour-old one. Frequency
	// factor reaches squash01(2) ≈ 0.667. Default with 14d half-life
	// would weight the year-old at ≈ 1e-8, giving frequency ≈ squash01(1)
	// ≈ 0.5. Concretely: disabled frequency > default frequency.
	disabled := Compute(makeInputs(-1 * time.Hour))
	if len(disabled.Hotspots) == 0 {
		t.Fatalf("expected hotspots when decay disabled; got 0")
	}
	if disabled.Hotspots[0].Factors["frequency"] <= def.Hotspots[0].Factors["frequency"] {
		t.Errorf("disabled.frequency=%v default.frequency=%v; disabled must outweigh stale-decay",
			disabled.Hotspots[0].Factors["frequency"], def.Hotspots[0].Factors["frequency"])
	}
}

// bd-iwqvb: lower-level decayWeight contract — negative half-life
// returns 1 for any age; zero is also coerced to "disabled" so direct
// callers of the helper see the same semantics as the high-level
// Inputs sentinel handling describes for negative durations.
func TestDecayWeight_NonPositiveHalfLifeReturnsFullWeight(t *testing.T) {
	t.Parallel()
	for _, half := range []time.Duration{-1, 0, -24 * time.Hour} {
		for _, age := range []time.Duration{0, time.Hour, 365 * 24 * time.Hour} {
			if got := decayWeight(age, half); got != 1 {
				t.Errorf("decayWeight(age=%v, half=%v) = %v, want 1", age, half, got)
			}
		}
	}
}

// Label-cluster bonus: two closed beads sharing a label and a path
// add weight to that path even without any reservation history.
func TestCompute_LabelClusterBonusFromClosedBeads(t *testing.T) {
	t.Parallel()
	now := clock()
	in := Inputs{
		Now: now,
		ClosedBeads: []ClosedBead{
			{ID: "bd-1", Labels: []string{"auth"}, Paths: []string{"internal/auth/session.go"}, ClosedAt: now.Add(-2 * time.Hour)},
			{ID: "bd-2", Labels: []string{"auth"}, Paths: []string{"internal/auth/session.go"}, ClosedAt: now.Add(-3 * time.Hour)},
		},
	}
	f := Compute(in)
	if len(f.Hotspots) == 0 {
		t.Fatal("no hotspots; label cluster did not surface")
	}
	if f.Hotspots[0].Factors["label_overlap"] == 0 {
		t.Errorf("label_overlap = 0, want > 0")
	}
}

// Git-touch activity surfaces a hotspot for files with no
// reservation history.
func TestCompute_GitActivitySurfacesUnreservedFiles(t *testing.T) {
	t.Parallel()
	now := clock()
	in := Inputs{
		Now: now,
		TouchedPaths: []TouchedPath{
			{Path: "internal/hot.go", TouchCount: 12, LastTouched: now.Add(-1 * time.Hour)},
		},
	}
	f := Compute(in)
	if len(f.Hotspots) != 1 {
		t.Fatalf("Hotspots = %d, want 1", len(f.Hotspots))
	}
	if f.Hotspots[0].PathPattern != "internal/hot.go" {
		t.Errorf("pattern = %s, want internal/hot.go", f.Hotspots[0].PathPattern)
	}
	if f.Hotspots[0].Factors["git_activity"] == 0 {
		t.Errorf("git_activity = 0, want > 0")
	}
}

func TestCompute_AlternativesProposedForBroadPatterns(t *testing.T) {
	t.Parallel()
	now := clock()
	in := Inputs{
		Now: now,
		Reservations: []ReservationEpisode{
			{PathPattern: "internal/**", AgentName: "A", AcquiredAt: now.Add(-1 * time.Hour), Conflicted: true},
		},
		TouchedPaths: []TouchedPath{
			{Path: "internal/auth/session.go", TouchCount: 8, LastTouched: now.Add(-1 * time.Hour)},
			{Path: "internal/billing/charge.go", TouchCount: 4, LastTouched: now.Add(-2 * time.Hour)},
		},
	}
	f := Compute(in)
	var top *Hotspot
	for i := range f.Hotspots {
		if f.Hotspots[i].PathPattern == "internal/**" {
			top = &f.Hotspots[i]
		}
	}
	if top == nil {
		t.Fatal("internal/** hotspot missing")
	}
	if len(top.Alternatives) == 0 {
		t.Errorf("Alternatives empty for broad pattern; want narrower suggestions")
	}
	hasAuth := false
	for _, a := range top.Alternatives {
		if strings.Contains(a, "internal/auth/") {
			hasAuth = true
			break
		}
	}
	if !hasAuth {
		t.Errorf("Alternatives = %v, want one mentioning internal/auth/", top.Alternatives)
	}
}

// bd-r8ybt: the catch-all patterns "**" and "/**" earn the maximum
// broad-glob penalty (0.6) but pre-fix produced no alternatives —
// "**" failed the HasSuffix("/**") check and "/**" produced an empty
// prefix that rejected every relative path. After the fix, these
// patterns derive alternatives from the top-level directory of each
// observed touch / bead path so an operator narrows from "**" to a
// concrete "<top_dir>/**".
func TestCompute_AlternativesProposedForCatchAllPattern(t *testing.T) {
	t.Parallel()
	now := clock()
	for _, pat := range []string{"**", "/**"} {
		t.Run(pat, func(t *testing.T) {
			in := Inputs{
				Now: now,
				Reservations: []ReservationEpisode{
					{PathPattern: pat, AgentName: "A", AcquiredAt: now.Add(-1 * time.Hour), Conflicted: true},
					{PathPattern: pat, AgentName: "B", AcquiredAt: now.Add(-30 * time.Minute), Conflicted: true},
				},
				TouchedPaths: []TouchedPath{
					{Path: "internal/auth/session.go", TouchCount: 8, LastTouched: now.Add(-1 * time.Hour)},
					{Path: "internal/auth/permissions.go", TouchCount: 5, LastTouched: now.Add(-2 * time.Hour)},
					{Path: "internal/parity/parity.go", TouchCount: 3, LastTouched: now.Add(-2 * time.Hour)},
				},
			}
			f := Compute(in)
			var top *Hotspot
			for i := range f.Hotspots {
				if f.Hotspots[i].PathPattern == pat {
					top = &f.Hotspots[i]
				}
			}
			if top == nil {
				t.Fatalf("hotspot for %q missing", pat)
			}
			if len(top.Alternatives) == 0 {
				t.Errorf("Alternatives empty for catch-all pattern %q; want narrower suggestions derived from top-level directories", pat)
			}
			// The "internal" top-level directory dominates the touched
			// paths (3 paths under internal/, only 1 of those under
			// internal/parity vs 2 under internal/auth). Catch-all
			// alternatives extract the FIRST path segment, so we
			// expect "internal/**" as a high-ranked suggestion.
			hasInternal := false
			for _, a := range top.Alternatives {
				if a == "internal/**" {
					hasInternal = true
					break
				}
			}
			if !hasInternal {
				t.Errorf("Alternatives = %v, want one to be 'internal/**' (top-level dir of every touched path)", top.Alternatives)
			}
		})
	}
}

func TestCompute_DeterministicSort(t *testing.T) {
	t.Parallel()
	now := clock()
	in := Inputs{
		Now: now,
		Reservations: []ReservationEpisode{
			{PathPattern: "z/**", AgentName: "A", AcquiredAt: now.Add(-1 * time.Hour), Conflicted: true},
			{PathPattern: "a/**", AgentName: "A", AcquiredAt: now.Add(-1 * time.Hour), Conflicted: true},
			{PathPattern: "m/**", AgentName: "A", AcquiredAt: now.Add(-1 * time.Hour), Conflicted: true},
		},
	}
	a, _ := json.Marshal(Compute(in))
	b, _ := json.Marshal(Compute(in))
	if string(a) != string(b) {
		t.Errorf("Compute output drifted between calls:\nfirst:  %s\nsecond: %s", a, b)
	}
}

// Confidence reflects sample count.
func TestCompute_ConfidenceTiers(t *testing.T) {
	t.Parallel()
	now := clock()
	build := func(n int) Inputs {
		eps := make([]ReservationEpisode, n)
		for i := 0; i < n; i++ {
			eps[i] = ReservationEpisode{
				PathPattern: "x/**",
				AgentName:   "A",
				AcquiredAt:  now.Add(-time.Duration(i+1) * time.Hour),
				Conflicted:  true,
			}
		}
		return Inputs{Reservations: eps, Now: now}
	}
	if c := Compute(build(2)).Hotspots[0].Confidence; c != ConfidenceLow {
		t.Errorf("2 samples confidence = %s, want low", c)
	}
	if c := Compute(build(8)).Hotspots[0].Confidence; c != ConfidenceMedium {
		t.Errorf("8 samples confidence = %s, want medium", c)
	}
	if c := Compute(build(25)).Hotspots[0].Confidence; c != ConfidenceHigh {
		t.Errorf("25 samples confidence = %s, want high", c)
	}
}

func pickAgent(i int) string {
	names := []string{"Alice", "Bob", "Carol"}
	return names[i%len(names)]
}
