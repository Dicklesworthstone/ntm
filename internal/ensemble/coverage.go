package ensemble

// CategoryCoverage stores per-category usage stats.
type CategoryCoverage struct {
	Category   ModeCategory
	TotalModes int
	UsedModes  []string
	Coverage   float64 // used/total
}

// CoverageReport summarizes coverage across categories.
type CoverageReport struct {
	Overall     float64
	PerCategory map[ModeCategory]CategoryCoverage
	BlindSpots  []ModeCategory
	Suggestions []string
}
