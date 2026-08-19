package ensemble

// Conflict captures a disagreement between two modes.
type Conflict struct {
	Topic      string
	ModeA      string
	ModeB      string
	PositionA  string
	PositionB  string
	Severity   ConflictSeverity
	Resolved   bool
	Resolution string
}

// ConflictDensity summarizes disagreement frequency across mode pairs.
type ConflictDensity struct {
	TotalConflicts      int
	ResolvedConflicts   int
	UnresolvedConflicts int
	ConflictsPerPair    float64
	HighConflictPairs   []string
	Source              string
}
