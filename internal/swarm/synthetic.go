package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

// SyntheticOutputPattern describes a fake agent's observable behavior.
type SyntheticOutputPattern string

const (
	SyntheticPatternIdle        SyntheticOutputPattern = "idle"
	SyntheticPatternWorking     SyntheticOutputPattern = "working"
	SyntheticPatternError       SyntheticOutputPattern = "error"
	SyntheticPatternRateLimit   SyntheticOutputPattern = "rate_limited"
	SyntheticPatternWaitingMail SyntheticOutputPattern = "waiting_for_mail"
	SyntheticPatternWriting     SyntheticOutputPattern = "writing_files"
	SyntheticPatternCompleted   SyntheticOutputPattern = "completed"
)

// SyntheticAgentState is the normalized state emitted by the harness.
type SyntheticAgentState string

const (
	SyntheticStateIdle        SyntheticAgentState = "idle"
	SyntheticStateWorking     SyntheticAgentState = "working"
	SyntheticStateError       SyntheticAgentState = "error"
	SyntheticStateRateLimit   SyntheticAgentState = "rate_limited"
	SyntheticStateWaitingMail SyntheticAgentState = "waiting_for_mail"
	SyntheticStateCompleted   SyntheticAgentState = "completed"
)

var defaultSyntheticPatterns = []SyntheticOutputPattern{
	SyntheticPatternIdle,
	SyntheticPatternWorking,
	SyntheticPatternError,
	SyntheticPatternRateLimit,
	SyntheticPatternWaitingMail,
	SyntheticPatternWriting,
	SyntheticPatternCompleted,
}

var syntheticAgentTypes = []string{"cc", "cod", "gmi"}

// SyntheticScenario configures an in-memory swarm run.
type SyntheticScenario struct {
	TestRunID             string                   `json:"test_run_id"`
	Name                  string                   `json:"name"`
	SessionName           string                   `json:"session_name"`
	PaneCount             int                      `json:"pane_count"`
	CommandCount          int                      `json:"command_count"`
	OutputLinesPerCommand int                      `json:"output_lines_per_command"`
	Patterns              []SyntheticOutputPattern `json:"patterns,omitempty"`
	StartTime             time.Time                `json:"start_time,omitempty"`
}

// SyntheticHarness runs deterministic fake swarm scenarios without tmux or model CLIs.
type SyntheticHarness struct {
	Logger *slog.Logger
}

// NewSyntheticHarness creates a synthetic swarm harness.
func NewSyntheticHarness(logger *slog.Logger) *SyntheticHarness {
	return &SyntheticHarness{Logger: logger}
}

// SyntheticRunResult is the machine-readable artifact from a synthetic run.
type SyntheticRunResult struct {
	Scenario    SyntheticScenario `json:"scenario"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt time.Time         `json:"completed_at"`
	Panes       []SyntheticPane   `json:"panes"`
	Events      []SyntheticEvent  `json:"events"`
	Metrics     SyntheticMetrics  `json:"metrics"`
}

// SyntheticPane is an in-memory fake tmux pane.
type SyntheticPane struct {
	ID           string                 `json:"id"`
	SessionName  string                 `json:"session_name"`
	Index        int                    `json:"index"`
	AgentType    string                 `json:"agent_type"`
	Pattern      SyntheticOutputPattern `json:"pattern"`
	State        SyntheticAgentState    `json:"state"`
	CommandCount int                    `json:"command_count"`
	EventCount   int                    `json:"event_count"`
	OutputTail   []string               `json:"output_tail"`
}

// SyntheticEvent records a fake pane event.
type SyntheticEvent struct {
	Seq           int                    `json:"seq"`
	Timestamp     time.Time              `json:"timestamp"`
	SessionName   string                 `json:"session_name"`
	PaneID        string                 `json:"pane_id"`
	PaneIndex     int                    `json:"pane_index"`
	AgentType     string                 `json:"agent_type"`
	Pattern       SyntheticOutputPattern `json:"pattern"`
	State         SyntheticAgentState    `json:"state"`
	Kind          string                 `json:"kind"`
	Message       string                 `json:"message"`
	LatencyMicros int64                  `json:"latency_micros"`
	OutputLines   []string               `json:"output_lines"`
}

// SyntheticMetrics summarizes harness scale and responsiveness.
type SyntheticMetrics struct {
	TestRunID               string `json:"test_run_id"`
	ScenarioName            string `json:"scenario_name"`
	SessionName             string `json:"session_name"`
	PaneCount               int    `json:"pane_count"`
	CommandCount            int    `json:"command_count"`
	EventCount              int    `json:"event_count"`
	LatencyP50Micros        int64  `json:"latency_p50_micros"`
	LatencyP95Micros        int64  `json:"latency_p95_micros"`
	LatencyMaxMicros        int64  `json:"latency_max_micros"`
	SyntheticDurationMicros int64  `json:"synthetic_duration_micros"`
	MemoryGrowthBytes       int64  `json:"memory_growth_bytes"`
	// Goroutines is the absolute goroutine count after the run.
	// Preserved alongside GoroutinesLeaked so a steady-state leak
	// (per-run delta = 0 but absolute count drifts upward) remains
	// observable, and so historical artifacts written under the
	// pre-bd-75unj schema parse cleanly.
	Goroutines       int `json:"goroutines"`
	GoroutinesLeaked int `json:"goroutines_leaked"`
}

// Run executes a deterministic in-memory scenario.
func (h *SyntheticHarness) Run(ctx context.Context, scenario SyntheticScenario) (*SyntheticRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	scenario = normalizeSyntheticScenario(scenario)
	if err := validateSyntheticScenario(scenario); err != nil {
		return nil, err
	}

	logger := syntheticLogger(h)
	startedAt := scenario.StartTime
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
		scenario.StartTime = startedAt
	}

	logger.Info("synthetic_swarm_start",
		"test_run_id", scenario.TestRunID,
		"session", scenario.SessionName,
		"scenario", scenario.Name,
		"pane_count", scenario.PaneCount,
		"command_count", scenario.CommandCount)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutinesBefore := runtime.NumGoroutine()

	result := &SyntheticRunResult{
		Scenario:  scenario,
		StartedAt: startedAt,
		Panes:     make([]SyntheticPane, 0, scenario.PaneCount),
		Events:    make([]SyntheticEvent, 0, scenario.PaneCount*scenario.CommandCount),
	}
	latencies := make([]int64, 0, scenario.PaneCount*scenario.CommandCount)
	seq := 0

	for paneIndex := 1; paneIndex <= scenario.PaneCount; paneIndex++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		pattern := scenario.Patterns[(paneIndex-1)%len(scenario.Patterns)]
		state := stateForSyntheticPattern(pattern)
		pane := SyntheticPane{
			ID:          fmt.Sprintf("%s:%d", scenario.SessionName, paneIndex),
			SessionName: scenario.SessionName,
			Index:       paneIndex,
			AgentType:   syntheticAgentTypes[(paneIndex-1)%len(syntheticAgentTypes)],
			Pattern:     pattern,
			State:       state,
		}

		var outputTail []string
		for commandIndex := 1; commandIndex <= scenario.CommandCount; commandIndex++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			seq++
			latency := syntheticLatencyMicros(paneIndex, commandIndex, pattern)
			latencies = append(latencies, latency)
			lines := syntheticOutputLines(scenario, pane, commandIndex)
			outputTail = append(outputTail, lines...)
			outputTail = lastSyntheticLines(outputTail, 12)

			result.Events = append(result.Events, SyntheticEvent{
				Seq:           seq,
				Timestamp:     startedAt.Add(time.Duration(seq) * time.Millisecond),
				SessionName:   scenario.SessionName,
				PaneID:        pane.ID,
				PaneIndex:     pane.Index,
				AgentType:     pane.AgentType,
				Pattern:       pattern,
				State:         state,
				Kind:          "pane_output",
				Message:       syntheticMessage(pattern, paneIndex, commandIndex),
				LatencyMicros: latency,
				OutputLines:   lines,
			})

			pane.CommandCount++
			pane.EventCount++
		}

		pane.OutputTail = outputTail
		result.Panes = append(result.Panes, pane)
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	goroutinesAfter := runtime.NumGoroutine()

	result.CompletedAt = startedAt.Add(time.Duration(seq) * time.Millisecond)
	result.Metrics = SyntheticMetrics{
		TestRunID:               scenario.TestRunID,
		ScenarioName:            scenario.Name,
		SessionName:             scenario.SessionName,
		PaneCount:               scenario.PaneCount,
		CommandCount:            scenario.CommandCount,
		EventCount:              seq,
		LatencyP50Micros:        syntheticPercentile(latencies, 50),
		LatencyP95Micros:        syntheticPercentile(latencies, 95),
		LatencyMaxMicros:        syntheticPercentile(latencies, 100),
		SyntheticDurationMicros: int64(result.CompletedAt.Sub(result.StartedAt) / time.Microsecond),
		MemoryGrowthBytes:       nonNegativeMemoryGrowth(before, after),
		Goroutines:              goroutinesAfter,
		GoroutinesLeaked:        nonNegativeIntDelta(goroutinesBefore, goroutinesAfter),
	}

	logger.Info("synthetic_swarm_complete",
		"test_run_id", scenario.TestRunID,
		"session", scenario.SessionName,
		"scenario", scenario.Name,
		"pane_count", result.Metrics.PaneCount,
		"event_count", result.Metrics.EventCount,
		"command_count", result.Metrics.CommandCount,
		"latency_p95_micros", result.Metrics.LatencyP95Micros,
		"memory_growth_bytes", result.Metrics.MemoryGrowthBytes,
		"goroutines", result.Metrics.Goroutines,
		"goroutines_leaked", result.Metrics.GoroutinesLeaked)

	return result, nil
}

// WriteArtifact writes the run result as stable indented JSON.
func (r *SyntheticRunResult) WriteArtifact(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func syntheticLogger(h *SyntheticHarness) *slog.Logger {
	if h != nil && h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func normalizeSyntheticScenario(s SyntheticScenario) SyntheticScenario {
	if s.Name == "" {
		s.Name = "synthetic-swarm"
	}
	if s.SessionName == "" {
		s.SessionName = "synthetic_" + sanitizeSyntheticID(s.Name)
	}
	if s.TestRunID == "" {
		s.TestRunID = fmt.Sprintf("%s-%dp-%dc", sanitizeSyntheticID(s.Name), s.PaneCount, s.CommandCount)
	}
	if s.PaneCount == 0 {
		s.PaneCount = 1
	}
	if s.CommandCount == 0 {
		s.CommandCount = 1
	}
	if s.OutputLinesPerCommand == 0 {
		s.OutputLinesPerCommand = 1
	}
	if len(s.Patterns) == 0 {
		s.Patterns = append([]SyntheticOutputPattern(nil), defaultSyntheticPatterns...)
	}
	return s
}

func validateSyntheticScenario(s SyntheticScenario) error {
	if s.PaneCount < 1 {
		return fmt.Errorf("pane count must be positive, got %d", s.PaneCount)
	}
	if s.CommandCount < 1 {
		return fmt.Errorf("command count must be positive, got %d", s.CommandCount)
	}
	if s.OutputLinesPerCommand < 1 {
		return fmt.Errorf("output lines per command must be positive, got %d", s.OutputLinesPerCommand)
	}
	if len(s.Patterns) == 0 {
		return fmt.Errorf("at least one synthetic output pattern is required")
	}
	for _, pattern := range s.Patterns {
		if !isKnownSyntheticPattern(pattern) {
			return fmt.Errorf("unknown synthetic output pattern %q", pattern)
		}
	}
	return nil
}

func isKnownSyntheticPattern(pattern SyntheticOutputPattern) bool {
	switch pattern {
	case SyntheticPatternIdle,
		SyntheticPatternWorking,
		SyntheticPatternError,
		SyntheticPatternRateLimit,
		SyntheticPatternWaitingMail,
		SyntheticPatternWriting,
		SyntheticPatternCompleted:
		return true
	default:
		return false
	}
}

func stateForSyntheticPattern(pattern SyntheticOutputPattern) SyntheticAgentState {
	switch pattern {
	case SyntheticPatternIdle:
		return SyntheticStateIdle
	case SyntheticPatternWorking, SyntheticPatternWriting:
		return SyntheticStateWorking
	case SyntheticPatternError:
		return SyntheticStateError
	case SyntheticPatternRateLimit:
		return SyntheticStateRateLimit
	case SyntheticPatternWaitingMail:
		return SyntheticStateWaitingMail
	case SyntheticPatternCompleted:
		return SyntheticStateCompleted
	default:
		return SyntheticStateError
	}
}

func syntheticLatencyMicros(paneIndex, commandIndex int, pattern SyntheticOutputPattern) int64 {
	base := int64(700 + paneIndex%17*37 + commandIndex%11*23)
	switch pattern {
	case SyntheticPatternIdle:
		return base
	case SyntheticPatternWorking:
		return base + 900
	case SyntheticPatternWriting:
		return base + 1300
	case SyntheticPatternWaitingMail:
		return base + 2100
	case SyntheticPatternRateLimit:
		return base + 3100
	case SyntheticPatternError:
		return base + 4100
	case SyntheticPatternCompleted:
		return base + 500
	default:
		return base
	}
}

func syntheticMessage(pattern SyntheticOutputPattern, paneIndex, commandIndex int) string {
	switch pattern {
	case SyntheticPatternIdle:
		return "idle: waiting for assignment"
	case SyntheticPatternWorking:
		return fmt.Sprintf("working: completed synthetic step %d", commandIndex)
	case SyntheticPatternWriting:
		return fmt.Sprintf("writing files: synthetic pane %d command %d", paneIndex, commandIndex)
	case SyntheticPatternWaitingMail:
		return "waiting for mail thread bd-synthetic"
	case SyntheticPatternRateLimit:
		return "rate-limited: retry after 60s"
	case SyntheticPatternError:
		return fmt.Sprintf("error: synthetic failure at command %d", commandIndex)
	case SyntheticPatternCompleted:
		return "completed synthetic task"
	default:
		return "unknown synthetic state"
	}
}

func syntheticOutputLines(s SyntheticScenario, pane SyntheticPane, commandIndex int) []string {
	lines := make([]string, 0, s.OutputLinesPerCommand)
	for lineIndex := 1; lineIndex <= s.OutputLinesPerCommand; lineIndex++ {
		lines = append(lines, fmt.Sprintf(
			"run=%s session=%s pane=%d agent=%s command=%d line=%d pattern=%s message=%s",
			s.TestRunID,
			s.SessionName,
			pane.Index,
			pane.AgentType,
			commandIndex,
			lineIndex,
			pane.Pattern,
			syntheticMessage(pane.Pattern, pane.Index, commandIndex),
		))
	}
	return lines
}

func lastSyntheticLines(lines []string, limit int) []string {
	if len(lines) <= limit {
		return append([]string(nil), lines...)
	}
	return append([]string(nil), lines[len(lines)-limit:]...)
}

func syntheticPercentile(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if percentile >= 100 {
		return sorted[len(sorted)-1]
	}
	if percentile <= 0 {
		return sorted[0]
	}
	index := int(math.Ceil(percentile/100*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func nonNegativeMemoryGrowth(before, after runtime.MemStats) int64 {
	growth := int64(after.Alloc) - int64(before.Alloc)
	if growth < 0 {
		return 0
	}
	return growth
}

func nonNegativeIntDelta(before, after int) int {
	if after <= before {
		return 0
	}
	return after - before
}

func sanitizeSyntheticID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '/' || r == '.':
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "synthetic"
	}
	return b.String()
}
