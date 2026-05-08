// Package pressure implements the NTM swarm pressure governor: a small,
// pluggable model that observes machine and swarm pressure (CPU, memory,
// process count, tmux pane activity, pipeline fan-out, rch queue/build
// slot pressure) and gates non-urgent actions when a budget is exceeded.
//
// The package is deliberately self-contained: providers are injected so
// tests run without gopsutil and runtime callers can plug in real probes
// later. See bd-2mb03.1 for design context and acceptance criteria.
package pressure

import (
	"sort"
	"time"
)

// Source identifies a pressure source.
type Source string

const (
	SourceCPU            Source = "cpu"
	SourceMemory         Source = "memory"
	SourceLoad           Source = "load"
	SourceProcCount      Source = "proc_count"
	SourcePaneActivity   Source = "pane_activity"
	SourcePipelineFanout Source = "pipeline_fanout"
	SourceRchQueue       Source = "rch_queue"
	SourceLocalBuild     Source = "local_build"
)

// Level classifies how loaded a pressure source is. Levels are ordered
// from least to most loaded; numerically larger means more loaded so
// callers can compare with `<` / `>` and use math.Max-style reductions.
type Level int

const (
	LevelLow Level = iota
	LevelNormal
	LevelElevated
	LevelHigh
	LevelCritical
)

// String renders a Level as a stable robot-JSON token.
func (l Level) String() string {
	switch l {
	case LevelLow:
		return "low"
	case LevelNormal:
		return "normal"
	case LevelElevated:
		return "elevated"
	case LevelHigh:
		return "high"
	case LevelCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Reading is a single observation from a Provider at a moment in time.
type Reading struct {
	Source Source  `json:"source"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit,omitempty"`
}

// Thresholds defines the value boundaries between Levels for one source.
// Boundaries are inclusive at the lower end: v >= Elevated => LevelElevated.
// All three values must be non-decreasing; Validate enforces that.
type Thresholds struct {
	Elevated float64 `json:"elevated"`
	High     float64 `json:"high"`
	Critical float64 `json:"critical"`
}

// DefaultThresholds returns the per-source thresholds shipped with NTM.
// These are tuned for a 64-core / 256GB host running a busy swarm; tests
// and config layers may override them per-source.
func DefaultThresholds() map[Source]Thresholds {
	return map[Source]Thresholds{
		SourceCPU:            {Elevated: 0.60, High: 0.80, Critical: 0.92},
		SourceMemory:         {Elevated: 0.65, High: 0.82, Critical: 0.92},
		SourceLoad:           {Elevated: 0.75, High: 1.00, Critical: 1.50},
		SourceProcCount:      {Elevated: 0.70, High: 0.85, Critical: 0.95},
		SourcePaneActivity:   {Elevated: 50, High: 100, Critical: 200},
		SourcePipelineFanout: {Elevated: 16, High: 32, Critical: 64},
		SourceRchQueue:       {Elevated: 0.60, High: 0.80, Critical: 0.95},
		SourceLocalBuild:     {Elevated: 4, High: 8, Critical: 16},
	}
}

// Classify reduces a raw reading value to a Level given thresholds.
// A value below Elevated is split between Low and Normal at Elevated/2.
func Classify(v float64, t Thresholds) Level {
	switch {
	case v >= t.Critical:
		return LevelCritical
	case v >= t.High:
		return LevelHigh
	case v >= t.Elevated:
		return LevelElevated
	case v >= t.Elevated/2:
		return LevelNormal
	default:
		return LevelLow
	}
}

// Snapshot is the aggregated pressure view at a point in time.
type Snapshot struct {
	TakenAt  time.Time        `json:"taken_at"`
	Readings []Reading        `json:"readings"`
	Levels   map[Source]Level `json:"-"`
	Overall  Level            `json:"-"`
	Limiting []Source         `json:"-"`
}

// SpawnAdmissionDecision is the robot-stable decision token for a
// pre-spawn admission check.
type SpawnAdmissionDecision string

const (
	SpawnAdmissionAdmit  SpawnAdmissionDecision = "admit"
	SpawnAdmissionDefer  SpawnAdmissionDecision = "defer"
	SpawnAdmissionRefuse SpawnAdmissionDecision = "refuse"
)

// SpawnAdmissionInput captures the signals used to decide whether a
// requested swarm spawn should proceed. Count limits are optional: zero
// means "not configured".
type SpawnAdmissionInput struct {
	Session             string
	RequestedAgents     int
	RequestedPanes      int
	SessionPanes        int
	CurrentPanes        int
	RunningAgents       int
	RunningSessions     int
	MaxAgents           int
	LargeSpawnThreshold int
	Pressure            Snapshot
}

// SpawnAdmission is the robot-stable explanation for a pre-spawn
// admission check.
type SpawnAdmission struct {
	Decision            SpawnAdmissionDecision `json:"decision"`
	Reason              string                 `json:"reason"`
	Hint                string                 `json:"hint,omitempty"`
	Session             string                 `json:"session,omitempty"`
	RequestedAgents     int                    `json:"requested_agents"`
	RequestedPanes      int                    `json:"requested_panes"`
	SessionPanes        int                    `json:"session_panes"`
	AdditionalPanes     int                    `json:"additional_panes"`
	CurrentPanes        int                    `json:"current_panes"`
	ProjectedPanes      int                    `json:"projected_panes"`
	RunningAgents       int                    `json:"running_agents"`
	RunningSessions     int                    `json:"running_sessions"`
	MaxAgents           int                    `json:"max_agents,omitempty"`
	AgentHeadroom       int                    `json:"agent_headroom,omitempty"`
	LargeSpawn          bool                   `json:"large_spawn"`
	LargeSpawnThreshold int                    `json:"large_spawn_threshold,omitempty"`
	PressureLevel       string                 `json:"pressure_level"`
	Limiting            []string               `json:"limiting,omitempty"`
	Sources             []SpawnAdmissionSource `json:"sources,omitempty"`
}

// SpawnAdmissionSource records the source-level pressure inputs used by
// a spawn admission check.
type SpawnAdmissionSource struct {
	Source string  `json:"source"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit,omitempty"`
	Level  string  `json:"level"`
}

// EvaluateSpawnAdmission converts pressure, pane, and agent-count
// signals into an admit/defer/refuse decision. It is deliberately pure
// so robot, session, and future scheduler callers can share the same
// threshold behavior.
func EvaluateSpawnAdmission(in SpawnAdmissionInput) SpawnAdmission {
	requestedAgents := maxInt(in.RequestedAgents, 0)
	requestedPanes := maxInt(in.RequestedPanes, 0)
	sessionPanes := maxInt(in.SessionPanes, 0)
	currentPanes := maxInt(in.CurrentPanes, 0)
	runningAgents := maxInt(in.RunningAgents, 0)
	runningSessions := maxInt(in.RunningSessions, 0)
	maxAgents := maxInt(in.MaxAgents, 0)
	largeThreshold := maxInt(in.LargeSpawnThreshold, 0)
	additionalPanes := requestedPanes - sessionPanes
	if additionalPanes < 0 {
		additionalPanes = 0
	}
	projectedPanes := currentPanes + additionalPanes
	largeSpawn := largeThreshold > 0 && requestedAgents >= largeThreshold
	pressureLevel := in.Pressure.Overall
	limiting := limitingStrings(in.Pressure.Limiting)

	out := SpawnAdmission{
		Decision:            SpawnAdmissionAdmit,
		Reason:              "headroom_available",
		Session:             in.Session,
		RequestedAgents:     requestedAgents,
		RequestedPanes:      requestedPanes,
		SessionPanes:        sessionPanes,
		AdditionalPanes:     additionalPanes,
		CurrentPanes:        currentPanes,
		ProjectedPanes:      projectedPanes,
		RunningAgents:       runningAgents,
		RunningSessions:     runningSessions,
		MaxAgents:           maxAgents,
		LargeSpawn:          largeSpawn,
		LargeSpawnThreshold: largeThreshold,
		PressureLevel:       pressureLevel.String(),
		Limiting:            limiting,
		Sources:             spawnAdmissionSources(in.Pressure),
	}
	if maxAgents > 0 {
		out.AgentHeadroom = maxAgents - requestedAgents
		if out.AgentHeadroom < 0 {
			out.AgentHeadroom = 0
		}
	}

	switch {
	case requestedAgents == 0 || requestedPanes == 0:
		out.Decision = SpawnAdmissionRefuse
		out.Reason = "invalid_request"
		out.Hint = "specify at least one agent"
	case maxAgents > 0 && requestedAgents > maxAgents:
		out.Decision = SpawnAdmissionRefuse
		out.Reason = "agent_limit_exceeded"
		out.Hint = "reduce requested agents or raise spawn_pacing agent caps"
	case largeSpawn && pressureLevel >= LevelCritical:
		out.Decision = SpawnAdmissionRefuse
		out.Reason = "pressure_critical"
		out.Hint = recommendation(ActionSwarmSpawn, in.Pressure.Limiting)
	case largeSpawn && pressureLevel >= LevelHigh:
		out.Decision = SpawnAdmissionDefer
		out.Reason = "pressure_high"
		out.Hint = recommendation(ActionSwarmSpawn, in.Pressure.Limiting)
	}
	return out
}

func spawnAdmissionSources(snap Snapshot) []SpawnAdmissionSource {
	if len(snap.Readings) == 0 {
		return nil
	}
	ordered := append([]Reading(nil), snap.Readings...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Source < ordered[j].Source })
	out := make([]SpawnAdmissionSource, 0, len(ordered))
	for _, r := range ordered {
		lvl := LevelLow
		if snap.Levels != nil {
			lvl = snap.Levels[r.Source]
		} else if thresholds, ok := DefaultThresholds()[r.Source]; ok {
			lvl = Classify(r.Value, thresholds)
		}
		out = append(out, SpawnAdmissionSource{
			Source: string(r.Source),
			Value:  r.Value,
			Unit:   r.Unit,
			Level:  lvl.String(),
		})
	}
	return out
}

// limitingSources returns the sources whose Level matches Overall, sorted
// alphabetically so the slice is deterministic for robot output.
func limitingSources(levels map[Source]Level, overall Level) []Source {
	if len(levels) == 0 {
		return nil
	}
	out := make([]Source, 0, len(levels))
	for src, lvl := range levels {
		if lvl == overall {
			out = append(out, src)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// buildSnapshot folds readings + thresholds into a Snapshot.
func buildSnapshot(now time.Time, readings []Reading, thresh map[Source]Thresholds) Snapshot {
	levels := make(map[Source]Level, len(readings))
	overall := LevelLow
	for _, r := range readings {
		t, ok := thresh[r.Source]
		if !ok {
			levels[r.Source] = LevelLow
			continue
		}
		lvl := Classify(r.Value, t)
		levels[r.Source] = lvl
		if lvl > overall {
			overall = lvl
		}
	}
	return Snapshot{
		TakenAt:  now,
		Readings: append([]Reading(nil), readings...),
		Levels:   levels,
		Overall:  overall,
		Limiting: limitingSources(levels, overall),
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
