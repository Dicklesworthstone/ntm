package robot

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
	ntmevents "github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// BackpressureOptions configures live and caller-supplied backpressure evaluation.
type BackpressureOptions struct {
	Session       string
	Thresholds    backpressure.Thresholds
	ExtraInputs   []backpressure.SurfaceInput
	CommandStats  []CommandBackpressureStats
	SkipLiveProbe bool
	Now           func() time.Time
}

// BackpressureOutput is the robot-readable envelope for overload snapshots,
// dashboard summaries, operator heatmaps, and load-shedding contracts.
type BackpressureOutput struct {
	RobotResponse
	Session      string                            `json:"session,omitempty"`
	Snapshot     backpressure.BackpressureSnapshot `json:"backpressure"`
	Dashboard    backpressure.DashboardSummary     `json:"dashboard"`
	Heatmap      backpressure.OperatorHeatmap      `json:"heatmap"`
	LoadShedding backpressure.LoadSheddingContract `json:"load_shedding"`
}

// CommandBackpressureStats describes one robot command execution pressure row.
type CommandBackpressureStats struct {
	Command       string
	Session       string
	Pane          string
	QueueDepth    int
	QueueCapacity int
	Latency       time.Duration
	SourceLoaded  bool
}

// GetBackpressure collects live backpressure observations across tmux capture,
// robot event dispatch, and optional caller-provided subsystem inputs (such as
// HTTP/SSE/WebSocket streamers), returning all four contract projections.
func GetBackpressure(opts BackpressureOptions) (*BackpressureOutput, error) {
	session := strings.TrimSpace(opts.Session)
	if session != "" {
		if err := tmux.ValidateSessionName(session); err != nil {
			out := &BackpressureOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("invalid session name %q: %w", session, err),
					ErrCodeInvalidFlag,
					"Provide a valid tmux session name or omit --session for host-wide inspection.",
				),
				Session: session,
			}
			EnsureArraysNeverNull(out)
			return out, nil
		}
	}

	thresholds := mergeBackpressureThresholds(opts.Thresholds)
	inputs := make([]backpressure.SurfaceInput, 0, 2+len(opts.CommandStats)+len(opts.ExtraInputs))
	if !opts.SkipLiveProbe {
		inputs = append(inputs, collectLiveBackpressureInputs(session, thresholds)...)
	}
	for _, stat := range opts.CommandStats {
		if !matchesBackpressureSession(session, stat.Session) {
			continue
		}
		inputs = append(inputs, backpressure.SurfaceInput{
			Surface:        backpressure.SurfaceRobot,
			Session:        strings.TrimSpace(stat.Session),
			Pane:           strings.TrimSpace(stat.Pane),
			Command:        strings.TrimSpace(stat.Command),
			QueueDepth:     stat.QueueDepth,
			QueueCapacity:  stat.QueueCapacity,
			LatencyMS:      stat.Latency.Milliseconds(),
			SourceLoaded:   stat.SourceLoaded,
			MissingWarning: "robot command backpressure counters were not loaded.",
		})
	}
	for _, extra := range opts.ExtraInputs {
		if !matchesBackpressureSession(session, extra.Session) {
			continue
		}
		inputs = append(inputs, extra)
	}

	snapshot := backpressure.Evaluate(inputs, backpressure.SnapshotOptions{
		Now:        opts.Now,
		Thresholds: thresholds,
	})
	resp := NewRobotResponse(true)
	if snapshot.Hint != "" {
		resp.Hint = snapshot.Hint
	}
	out := &BackpressureOutput{
		RobotResponse: resp,
		Session:       session,
		Snapshot:      snapshot,
		Dashboard:     snapshot.Dashboard(),
		Heatmap:       snapshot.OperatorHeatmap(),
		LoadShedding:  snapshot.LoadSheddingContract(),
	}
	EnsureArraysNeverNull(out)
	return out, nil
}

// PrintBackpressure handles the --robot-backpressure flag.
func PrintBackpressure(opts BackpressureOptions) error {
	output, err := GetBackpressure(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot backpressure failed")
}

func mergeBackpressureThresholds(custom backpressure.Thresholds) backpressure.Thresholds {
	if custom == (backpressure.Thresholds{}) {
		return backpressure.DefaultThresholds()
	}
	return custom
}

func matchesBackpressureSession(filter, candidate string) bool {
	filter = strings.TrimSpace(filter)
	candidate = strings.TrimSpace(candidate)
	if filter == "" || candidate == "" {
		return true
	}
	return filter == candidate
}

func collectLiveBackpressureInputs(session string, thresholds backpressure.Thresholds) []backpressure.SurfaceInput {
	inputs := make([]backpressure.SurfaceInput, 0, 4)

	start := time.Now()
	if tmux.DefaultClient == nil || !tmux.DefaultClient.IsInstalled() {
		inputs = append(inputs, backpressure.SurfaceInput{
			Surface:        backpressure.SurfaceTmuxCapture,
			Session:        session,
			SourceLoaded:   false,
			MissingWarning: "tmux binary is not installed; live pane capture pressure is unavailable.",
		})
	} else {
		sessions, err := tmux.DefaultClient.ListSessions()
		listLatencyMS := time.Since(start).Milliseconds()
		if err != nil {
			inputs = append(inputs, backpressure.SurfaceInput{
				Surface:        backpressure.SurfaceTmuxCapture,
				Session:        session,
				LatencyMS:      listLatencyMS,
				SourceLoaded:   false,
				MissingWarning: fmt.Sprintf("tmux session enumeration unavailable: %v", err),
			})
		} else if session != "" {
			found := false
			for _, s := range sessions {
				if s.Name == session {
					found = true
					break
				}
			}
			if !found {
				inputs = append(inputs, backpressure.SurfaceInput{
					Surface:        backpressure.SurfaceTmuxCapture,
					Session:        session,
					LatencyMS:      listLatencyMS,
					SourceLoaded:   false,
					MissingWarning: fmt.Sprintf("tmux session %q is not running.", session),
				})
			} else {
				panes, paneErr := tmux.DefaultClient.GetPanes(session)
				totalLatencyMS := time.Since(start).Milliseconds()
				if paneErr != nil {
					inputs = append(inputs, backpressure.SurfaceInput{
						Surface:        backpressure.SurfaceTmuxCapture,
						Session:        session,
						LatencyMS:      totalLatencyMS,
						SourceLoaded:   false,
						MissingWarning: fmt.Sprintf("tmux pane enumeration failed for %q: %v", session, paneErr),
					})
				} else {
					inputs = append(inputs, backpressure.SurfaceInput{
						Surface:       backpressure.SurfaceTmuxCapture,
						Session:       session,
						QueueDepth:    len(panes),
						QueueCapacity: thresholds.QueueDepthCritical,
						LatencyMS:     totalLatencyMS,
						SourceLoaded:  true,
					})
				}
			}
		} else if len(sessions) == 0 {
			inputs = append(inputs, backpressure.SurfaceInput{
				Surface:       backpressure.SurfaceTmuxCapture,
				QueueDepth:    0,
				QueueCapacity: thresholds.QueueDepthCritical,
				LatencyMS:     listLatencyMS,
				SourceLoaded:  true,
			})
		} else {
			for _, s := range sessions {
				paneStart := time.Now()
				panes, paneErr := tmux.DefaultClient.GetPanes(s.Name)
				paneLatencyMS := listLatencyMS + time.Since(paneStart).Milliseconds()
				if paneErr != nil {
					inputs = append(inputs, backpressure.SurfaceInput{
						Surface:        backpressure.SurfaceTmuxCapture,
						Session:        s.Name,
						LatencyMS:      paneLatencyMS,
						SourceLoaded:   false,
						MissingWarning: fmt.Sprintf("tmux pane enumeration failed for %q: %v", s.Name, paneErr),
					})
					continue
				}
				inputs = append(inputs, backpressure.SurfaceInput{
					Surface:       backpressure.SurfaceTmuxCapture,
					Session:       s.Name,
					QueueDepth:    len(panes),
					QueueCapacity: thresholds.QueueDepthCritical,
					LatencyMS:     paneLatencyMS,
					SourceLoaded:  true,
				})
			}
		}
	}

	inflight, semUsed, semCap := ntmevents.DefaultBus.QueueMetrics()
	busDepth := inflight
	if semUsed > busDepth {
		busDepth = semUsed
	}
	inputs = append(inputs, backpressure.SurfaceInput{
		Surface:        backpressure.SurfaceRobot,
		Session:        session,
		Command:        "event-bus",
		QueueDepth:     busDepth,
		QueueCapacity:  semCap,
		SourceLoaded:   ntmevents.DefaultBus != nil,
		MissingWarning: "default event bus is not initialized.",
	})

	return inputs
}

func backpressureCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "backpressure",
		Flag:        "--robot-backpressure",
		Category:    "state",
		Description: "Evaluate live subsystem backpressure, dashboard summary, operator heatmap, and load-shedding contract.",
		Parameters: []RobotParameter{
			{Name: "session", Flag: "--session", Type: "string", Required: false, Description: "Filter live backpressure inspection to a specific tmux session"},
		},
		Examples: []string{
			"ntm --robot-backpressure",
			"ntm --robot-backpressure --session=myproject",
		},
	}
}
