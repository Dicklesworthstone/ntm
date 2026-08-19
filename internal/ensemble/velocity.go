package ensemble

// VelocityEntry captures per-mode velocity stats.
type VelocityEntry struct {
	ModeID         string
	ModeName       string
	TokensSpent    int
	FindingsCount  int
	UniqueFindings int
	Velocity       float64 // findings per 1k tokens
}

// VelocityReport summarizes velocity across modes.
type VelocityReport struct {
	Overall        float64
	PerMode        []VelocityEntry
	HighPerformers []string
	LowPerformers  []string
	Suggestions    []string
}
