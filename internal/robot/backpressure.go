package robot

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

// BackpressureOutput is the robot-readable envelope for overload snapshots.
type BackpressureOutput struct {
	RobotResponse
	Snapshot backpressure.BackpressureSnapshot `json:"backpressure"`
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

// CommandBackpressureInput converts robot command timing and queue state to the
// shared backpressure taxonomy.
func CommandBackpressureInput(stats CommandBackpressureStats) backpressure.SurfaceInput {
	loaded := stats.SourceLoaded
	if !loaded && (stats.Command != "" || stats.Session != "" || stats.Pane != "" || stats.Latency > 0 || stats.QueueDepth > 0 || stats.QueueCapacity > 0) {
		loaded = true
	}
	input := backpressure.SurfaceInput{
		Surface:       backpressure.SurfaceRobot,
		Session:       stats.Session,
		Pane:          stats.Pane,
		Command:       stats.Command,
		QueueDepth:    stats.QueueDepth,
		QueueCapacity: stats.QueueCapacity,
		LatencyMS:     stats.Latency.Milliseconds(),
		SourceLoaded:  loaded,
	}
	if !loaded {
		input.MissingWarning = "robot command queue metrics are not available."
	}
	return input
}

// BuildBackpressureOutput wraps a snapshot in the standard robot envelope and
// carries retry/degrade hints into the top-level error fields.
func BuildBackpressureOutput(snapshot backpressure.BackpressureSnapshot) BackpressureOutput {
	resp := NewRobotResponse(snapshot.Success)
	if !snapshot.Success {
		resp.Error = "backpressure detected"
		resp.ErrorCode = ErrCodeResourceBusy
		resp.Hint = snapshot.Hint
		resp.Meta = NewResponseMeta("robot-backpressure").WithExitCode(1)
	} else {
		resp.Meta = NewResponseMeta("robot-backpressure").WithExitCode(0)
	}
	return BackpressureOutput{
		RobotResponse: resp,
		Snapshot:      snapshot,
	}
}
